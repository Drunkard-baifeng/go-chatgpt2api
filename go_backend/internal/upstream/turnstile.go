package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 此 VM 只解释协议样本中的寄存器指令和白名单函数，不执行 JavaScript，
// 不访问文件、网络、浏览器或真实 localStorage。步骤、递归和数据量全局有界。
type vmFunction func([]any) (any, error)
type orderedObject struct {
	keys   []string
	values map[string]any
}
type sentinelVM struct {
	ctx          context.Context
	registers    map[float64]any
	steps, depth int
	allocated    int
	started      time.Time
	result       string
	lastOpcode   float64
}

func xorString(text, key string) string {
	if key == "" {
		return text
	}
	a, b := []rune(text), []rune(key)
	for i := range a {
		a[i] ^= b[i%len(b)]
	}
	return string(a)
}
func solveTurnstile(parent context.Context, dx, p string) (string, error) {
	if len(dx) == 0 || len(dx) > 1<<20 || len(p) == 0 || len(p) > 65536 {
		return "", &Failure{Stage: "requirements_turnstile", Code: "turnstile_input_limit", Status: 502}
	}
	decoded, err := base64.StdEncoding.DecodeString(dx)
	if err != nil || !utf8.Valid(decoded) {
		return "", &Failure{Stage: "requirements_turnstile", Code: "turnstile_decode_failed", Status: 502}
	}
	plain := xorString(string(decoded), p)
	var queue []any
	decoder := json.NewDecoder(strings.NewReader(plain))
	decoder.UseNumber()
	if decoder.Decode(&queue) != nil || len(queue) > 20000 {
		return "", &Failure{Stage: "requirements_turnstile", Code: "turnstile_program_invalid", Status: 502}
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	vm := &sentinelVM{ctx: ctx, registers: map[float64]any{}, started: time.Now()}
	for _, opcode := range []int{1, 2, 3, 5, 6, 7, 8, 11, 12, 13, 14, 15, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 33, 34} {
		vm.registers[float64(opcode)] = vmFunction(func(args []any) (any, error) { return nil, vm.operation(opcode, args) })
	}
	vm.registers[9] = queue
	vm.registers[10] = "window"
	vm.registers[16] = p
	if err := vm.run(); err != nil {
		if err == ErrChallenge {
			return "", &Failure{Stage: "requirements_turnstile", Code: "turnstile_execution_limit", Status: 502}
		}
		return "", err
	}
	if vm.result == "" {
		return "", &Failure{Stage: "requirements_turnstile", Code: "turnstile_result_missing", Status: 502}
	}
	return vm.result, nil
}
func (vm *sentinelVM) get(key any) any {
	id, ok := registerID(key)
	if !ok {
		return nil
	}
	return vm.registers[id]
}
func registerID(key any) (float64, bool) {
	value, ok := number(key)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}
func (vm *sentinelVM) set(key, value any) error {
	id, ok := registerID(key)
	if !ok {
		return ErrChallenge
	}
	switch v := value.(type) {
	case string:
		if len(v) > 1<<20 {
			return ErrChallenge
		}
	case []any:
		if len(v) > 20000 {
			return ErrChallenge
		}
	}
	switch v := value.(type) {
	case string:
		vm.allocated += len(v)
	case []any:
		vm.allocated += len(v) * 16
	default:
		vm.allocated += 16
	}
	if vm.allocated > 8<<20 {
		return ErrChallenge
	}
	if len(vm.registers) >= 20000 {
		return ErrChallenge
	}
	vm.registers[id] = value
	return nil
}
func (vm *sentinelVM) run() error {
	vm.depth++
	defer func() { vm.depth-- }()
	if vm.depth > 32 {
		return ErrChallenge
	}
	for {
		if err := vm.ctx.Err(); err != nil {
			return err
		}
		queue, ok := vm.registers[9].([]any)
		if !ok || len(queue) == 0 {
			return nil
		}
		vm.steps++
		if vm.steps > 20000 {
			return ErrChallenge
		}
		vm.registers[9] = queue[1:]
		instruction, ok := queue[0].([]any)
		if !ok || len(instruction) == 0 {
			continue
		}
		vm.lastOpcode, _ = registerID(instruction[0])
		if _, err := vm.invoke(vm.get(instruction[0]), instruction[1:]); err != nil {
			return err
		}
	}
}
func (vm *sentinelVM) invoke(target any, args []any) (any, error) {
	if fn, ok := target.(vmFunction); ok {
		return fn(args)
	}
	name, ok := target.(string)
	if !ok {
		return nil, nil
	}
	switch name {
	case "window.performance.now":
		return float64(time.Since(vm.started).Nanoseconds())/1e6 + rand.Float64()/1e6, nil
	case "window.Math.random":
		return rand.Float64(), nil
	case "window.Object.create":
		return &orderedObject{values: map[string]any{}}, nil
	case "window.Object.keys":
		if len(args) == 0 {
			return nil, nil
		}
		if args[0] == "window.localStorage" {
			return []any{"STATSIG_LOCAL_STORAGE_INTERNAL_STORE_V4", "STATSIG_LOCAL_STORAGE_STABLE_ID", "client-correlated-secret", "oai/apps/capExpiresAt", "oai-did", "STATSIG_LOCAL_STORAGE_LOGGING_REQUEST", "UiState.isNavigationCollapsed.1"}, nil
		}
		if object, ok := args[0].(*orderedObject); ok {
			keys := make([]any, len(object.keys))
			for i, k := range object.keys {
				keys[i] = k
			}
			return keys, nil
		}
	case "window.Reflect.set":
		if len(args) < 3 {
			return false, nil
		}
		if object, ok := args[0].(*orderedObject); ok {
			key := vmString(args[1])
			if len(key) > 4096 || len(object.keys) >= 20000 {
				return nil, ErrChallenge
			}
			vm.allocated += len(key) + 16
			if vm.allocated > 8<<20 {
				return nil, ErrChallenge
			}
			if _, ok := object.values[key]; !ok {
				object.keys = append(object.keys, key)
			}
			object.values[key] = args[2]
			return true, nil
		}
		return false, nil
	}
	return nil, nil
}
func (vm *sentinelVM) property(object, key any) any {
	switch object := object.(type) {
	case *orderedObject:
		return object.values[vmString(key)]
	case map[float64]any:
		id, ok := registerID(key)
		if ok {
			return object[id]
		}
	case map[string]any:
		return object[vmString(key)]
	case []any:
		index, ok := registerID(key)
		if ok && index >= 0 && index < float64(len(object)) && math.Trunc(index) == index {
			return object[int(index)]
		}
	case string:
		k := vmString(key)
		if object == "window.document" && k == "location" {
			return "https://chatgpt.com/"
		}
		if k != "" && k != "undefined" && k != "None" {
			return object + "." + k
		}
	}
	return nil
}
func (vm *sentinelVM) operation(op int, args []any) error {
	at := func(i int) any {
		if i < len(args) {
			return args[i]
		}
		return nil
	}
	get := func(i int) any { return vm.get(at(i)) }
	callArgs := func(start int) []any {
		values := []any{}
		for _, key := range args[start:] {
			values = append(values, vm.get(key))
		}
		return values
	}
	set := func(value any) error { return vm.set(at(0), value) }
	switch op {
	case 1:
		return set(xorString(vmString(get(0)), vmString(get(1))))
	case 2:
		return set(at(1))
	case 3:
		text, ok := at(0).(string)
		if !ok {
			return nil
		}
		if len(text) > 65536 {
			return ErrChallenge
		}
		vm.result = base64.StdEncoding.EncodeToString([]byte(text))
		return nil
	case 5:
		if current, ok := get(0).([]any); ok {
			return set(append(append([]any{}, current...), get(1)))
		}
		_, a := get(0).(string)
		_, b := get(1).(string)
		_, c := number(get(0))
		_, d := number(get(1))
		if a || b || c || d {
			left, right := vmString(get(0)), vmString(get(1))
			if len(left)+len(right) > 1<<20 {
				return ErrChallenge
			}
			return set(left + right)
		}
		return set("NaN")
	case 6, 24:
		return set(vm.property(get(1), get(2)))
	case 7:
		if len(args) < 1 {
			return nil
		}
		_, err := vm.invoke(get(0), callArgs(1))
		return err
	case 8, 34:
		return set(get(1))
	case 11:
		return set(nil)
	case 12:
		return set(vm.registers)
	case 13:
		if len(args) < 2 {
			return nil
		}
		_, err := vm.invoke(get(1), args[2:])
		return err
	case 14:
		text, ok := get(1).(string)
		if !ok || len(text) > 1<<20 {
			return nil
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return nil
		}
		return set(value)
	case 15:
		encoded, err := vm.boundedJSON(get(1))
		if err != nil {
			return err
		}
		return set(string(encoded))
	case 17:
		if len(args) < 2 {
			return nil
		}
		value, err := vm.invoke(get(1), callArgs(2))
		if err != nil {
			return err
		}
		return set(value)
	case 18:
		value, err := base64.StdEncoding.DecodeString(vmString(get(0)))
		if err != nil || !utf8.Valid(value) {
			return nil
		}
		return set(string(value))
	case 19:
		return set(base64.StdEncoding.EncodeToString([]byte(vmString(get(0)))))
	case 20:
		if len(args) >= 3 && reflect.DeepEqual(get(0), get(1)) {
			_, err := vm.invoke(get(2), args[3:])
			return err
		}
	case 21:
		if len(args) < 4 {
			return nil
		}
		a, _ := number(get(0))
		b, _ := number(get(1))
		n, _ := number(get(2))
		if math.Abs(a-b) > math.Abs(n) {
			_, err := vm.invoke(get(3), args[4:])
			return err
		}
	case 22:
		queue, ok := at(1).([]any)
		if !ok {
			return nil
		}
		previous := vm.registers[9]
		vm.registers[9] = append([]any{}, queue...)
		err := vm.run()
		vm.registers[9] = previous
		if err != nil {
			return err
		}
		return set("None")
	case 23:
		if len(args) >= 2 && get(0) != nil {
			_, err := vm.invoke(get(1), args[2:])
			return err
		}
	case 25, 26, 28:
		return nil
	case 27:
		if current, ok := get(0).([]any); ok {
			for i, value := range current {
				if reflect.DeepEqual(value, get(1)) {
					return set(append(append([]any{}, current[:i]...), current[i+1:]...))
				}
			}
			return nil
		}
		a, _ := number(get(0))
		b, _ := number(get(1))
		return set(a - b)
	case 29:
		a, okA := number(get(1))
		b, okB := number(get(2))
		return set(okA && okB && a < b)
	case 30:
		captures := []any{}
		queue, ok := at(2).([]any)
		if extended, yes := at(3).([]any); yes {
			captures, _ = at(2).([]any)
			queue = extended
			ok = true
		}
		if !ok {
			return nil
		}
		queue = append([]any{}, queue...)
		return set(vmFunction(func(values []any) (any, error) {
			previous := vm.registers[9]
			for i, key := range captures {
				if i < len(values) {
					if err := vm.set(key, values[i]); err != nil {
						return nil, err
					}
				}
			}
			vm.registers[9] = append([]any{}, queue...)
			err := vm.run()
			vm.registers[9] = previous
			return nil, err
		}))
	case 33:
		a, _ := number(get(1))
		b, _ := number(get(2))
		return set(a * b)
	}
	return nil
}

// 先按展开后的树计费，再交给标准 JSON 编码；共享 DAG 也不能绕过上限。
func (vm *sentinelVM) boundedJSON(value any) ([]byte, error) {
	nodes, bytes := 0, 0
	var copyValue func(any, int) (any, error)
	copyValue = func(value any, depth int) (any, error) {
		if err := vm.ctx.Err(); err != nil {
			return nil, err
		}
		nodes++
		bytes += 8
		if nodes > 20000 || bytes > 1<<20 || depth > 32 {
			return nil, ErrChallenge
		}
		switch item := value.(type) {
		case nil, bool, float64, int, json.Number:
			return item, nil
		case string:
			bytes += len(item)*6 + 2
			if bytes > 1<<20 {
				return nil, ErrChallenge
			}
			return item, nil
		case []any:
			if len(item) > 20000-nodes {
				return nil, ErrChallenge
			}
			out := make([]any, len(item))
			for i, v := range item {
				next, err := copyValue(v, depth+1)
				if err != nil {
					return nil, err
				}
				out[i] = next
			}
			return out, nil
		case map[string]any:
			if len(item) > 20000-nodes {
				return nil, ErrChallenge
			}
			out := make(map[string]any, len(item))
			for k, v := range item {
				bytes += len(k)*6 + 4
				next, err := copyValue(v, depth+1)
				if err != nil {
					return nil, err
				}
				out[k] = next
			}
			return out, nil
		case *orderedObject:
			if len(item.keys) > 20000-nodes {
				return nil, ErrChallenge
			}
			out := make(map[string]any, len(item.keys))
			for _, k := range item.keys {
				bytes += len(k)*6 + 4
				next, err := copyValue(item.values[k], depth+1)
				if err != nil {
					return nil, err
				}
				out[k] = next
			}
			return out, nil
		default:
			return nil, ErrChallenge
		}
	}
	bounded, err := copyValue(value, 0)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(bounded)
	if err != nil || len(encoded) > 1<<20 {
		return nil, ErrChallenge
	}
	return encoded, nil
}
func number(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case json.Number:
		n, err := value.Float64()
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(value, 64)
		return n, err == nil
	}
	return 0, false
}
func vmString(value any) string {
	if value == nil {
		return "undefined"
	}
	switch value := value.(type) {
	case string:
		switch value {
		case "window.Math":
			return "[object Math]"
		case "window.Reflect":
			return "[object Reflect]"
		case "window.performance":
			return "[object Performance]"
		case "window.localStorage":
			return "[object Storage]"
		case "window.Object":
			return "function Object() { [native code] }"
		case "window.Reflect.set":
			return "function set() { [native code] }"
		case "window.performance.now":
			return "function () { [native code] }"
		case "window.Object.create":
			return "function create() { [native code] }"
		case "window.Object.keys":
			return "function keys() { [native code] }"
		case "window.Math.random":
			return "function random() { [native code] }"
		}
		return value
	case json.Number:
		return value.String()
	case float64:
		s := strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s
	case bool:
		if value {
			return "True"
		}
		return "False"
	case []any:
		parts := []string{}
		size := 0
		for _, item := range value {
			s, ok := item.(string)
			if !ok {
				return "[object Array]"
			}
			size += len(s) + 1
			if size > 1<<20 {
				return "[object Array]"
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}
