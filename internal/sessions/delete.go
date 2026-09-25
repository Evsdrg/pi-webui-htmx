package sessions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"pi-bridge-go/internal/protocol"
)

// DeleteResult 描述一次删除的结果。
type DeleteResult struct {
	SessionID string `json:"sessionId"`
	Trashed   bool   `json:"trashed"`
	Path      string `json:"path"`
}

// Delete 删除一个会话文件。
// 优先使用 trash 命令，避免永久丢失；没有 trash 时才真正删除。
// 只允许删除受管目录内的文件，且必须与索引中的记录一致。
func (s *Store) Delete(ctx context.Context, id string) (DeleteResult, error) {
	e, err := s.index.Lookup(ctx, id)
	if err != nil {
		return DeleteResult{}, err
	}
	path := filepath.Join(s.dir, filepath.FromSlash(e.path))
	// 再次确认解析后的路径仍在受管根内，防止索引与磁盘不一致。
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return DeleteResult{}, protocol.E("not_found", "会话文件不存在")
	}
	rootReal, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return DeleteResult{}, protocol.E("pi_error", "无法解析会话目录")
	}
	rel, err := filepath.Rel(rootReal, real)
	if err != nil || rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
		return DeleteResult{}, protocol.E("forbidden", "会话文件不在受管目录内")
	}
	if err := ctx.Err(); err != nil {
		return DeleteResult{}, protocol.E("timeout", "删除被取消")
	}

	trashed := false
	if _, lookErr := exec.LookPath("trash"); lookErr == nil {
		cmd := exec.CommandContext(ctx, "trash", path)
		if err := cmd.Run(); err == nil {
			trashed = true
		}
	}
	if !trashed {
		if err := os.Remove(path); err != nil {
			return DeleteResult{}, protocol.E("pi_error", "删除会话文件失败")
		}
	}
	// 让索引尽快反映删除结果。
	s.index.Invalidate()
	return DeleteResult{SessionID: id, Trashed: trashed, Path: path}, nil
}
