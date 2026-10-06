package organize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/local/115-direct/internal/store"
)

type rootIdentity struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func CheckRoots(ctx context.Context, st *store.Store, c DirectoryConfig) error {
	if err := ValidateRoots(c); err != nil {
		return err
	}
	for _, path := range []string{c.PendingPath, c.STRMPath} {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		real, err = filepath.Abs(real)
		if err != nil {
			return err
		}
		info, err := os.Stat(real)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("目录文件系统标识不可读取")
		}
		identity := rootIdentity{real, uint64(stat.Dev), stat.Ino}
		key := "root_identity:" + stableSuffix(real)
		var old rootIdentity
		if err := st.GetSetting(ctx, key, &old); errors.Is(err, sql.ErrNoRows) {
			if err := st.PutSetting(ctx, key, identity); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if old != identity {
			return fmt.Errorf("目录挂载标识已变化，停止写入并等待管理员核对: %s", real)
		}
	}
	return nil
}
