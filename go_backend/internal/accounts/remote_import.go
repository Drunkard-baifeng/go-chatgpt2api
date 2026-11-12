package accounts

import "context"

// WithProcessing 让远程凭据拉取复用账号维护的唯一并发容量。
// fn 结束前占用一个名额；调用者不得在 fn 内发起本地批量账号变更。
func (r *Repository) WithProcessing(ctx context.Context, fn func(context.Context) error) error {
	release, err := r.waitProcessing(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(ctx)
}
