import { spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 统一入口：构建同源控制台、准备 embed 资源，再编译单文件服务。
const root = fileURLToPath(new URL('../', import.meta.url))
const frontend = path.join(root, 'new_frontend')
const backend = path.join(root, 'go_backend')
const embedded = path.join(backend, 'internal', 'app', 'console', 'dist')
const args = process.argv.slice(2)
const prepareOnly = args[0] === '--prepare' && args.length === 1
if (args.length && !prepareOnly) {
  console.error('用法：node scripts/build.mjs [--prepare]；GOOS、GOARCH 可指定目标平台。')
  process.exit(1)
}

function run(command, argv, cwd, extraEnv = {}) {
  const result = spawnSync(command, argv, {
    cwd, stdio: 'inherit', env: { ...process.env, ...extraEnv },
  })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`构建步骤失败：${command}（${result.status ?? result.signal}）`)
}

function npm(argv) {
  // Windows 的 npm.cmd 通过系统命令解释器执行，参数均为固定构建命令。
  if (process.platform === 'win32') {
    run('cmd.exe', ['/d', '/s', '/c', `npm ${argv.join(' ')}`], frontend, { VITE_API_URL: '' })
  } else {
    run('npm', argv, frontend, { VITE_API_URL: '' })
  }
}

try {
  const minimum = readFileSync(path.join(frontend, '.node-version'), 'utf8').trim().replace(/^v/, '').split('.').map(Number)
  const current = process.versions.node.split('.').map(Number)
  if (current[0] !== minimum[0] || current[1] < minimum[1] || (current[1] === minimum[1] && current[2] < minimum[2])) {
    throw new Error(`请使用 Node.js ${minimum.join('.')} 或同一主版本的更新补丁。`)
  }
  // 提前清除固定的生成目录，失败时不能误用上一次构建资源。
  if (path.relative(backend, embedded) !== path.join('internal', 'app', 'console', 'dist')) {
    throw new Error('控制台资源目录越界')
  }
  rmSync(embedded, { recursive: true, force: true })
  npm(['run', 'build'])
  const dist = path.join(frontend, 'dist')
  if (!existsSync(path.join(dist, 'index.html'))) throw new Error('前端没有生成入口文件')
  cpSync(dist, embedded, { recursive: true })
  if (prepareOnly) {
    console.log('控制台资源已准备，可执行 go test、go vet 或 go build。')
  } else {
    const targetOS = process.env.GOOS || (process.platform === 'win32' ? 'windows' : process.platform)
    const targetArch = process.env.GOARCH || (process.arch === 'x64' ? 'amd64' : process.arch)
    const name = `chatgpt2api-${targetOS}-${targetArch}${targetOS === 'windows' ? '.exe' : ''}`
    const output = path.join(root, 'bin', name)
    mkdirSync(path.dirname(output), { recursive: true })
    run('go', ['build', '-mod=readonly', '-trimpath', '-ldflags=-s -w', '-o', output, './cmd/chatgpt2api'], backend, { GOWORK: 'off', CGO_ENABLED: '0' })
    console.log(`已生成含控制台的可执行文件：${output}`)
  }
} catch (error) {
  console.error(error.message)
  process.exitCode = 1
}
