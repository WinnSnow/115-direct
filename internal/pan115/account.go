package pan115

import (
	"context"
	"time"
)

const accountCacheTTL = 5 * time.Minute

func (p *DriverProvider) Account(ctx context.Context, refresh bool) (*AccountInfo, error) {
	p.accountMu.Lock()
	defer p.accountMu.Unlock()
	now := time.Now()
	if !refresh && p.account != nil && now.Sub(p.accountAt) < accountCacheTTL {
		cached := *p.account
		return &cached, nil
	}
	c, _, err := p.current()
	if err != nil {
		return nil, err
	}
	user, err := callContext(ctx, c.GetUser)
	if err != nil {
		return nil, err
	}
	info, err := callContext(ctx, c.GetInfo)
	if err != nil {
		return nil, err
	}
	account := &AccountInfo{
		UserID:          user.UserID,
		Username:        user.UserName,
		VIP:             user.Vip > 0,
		VIPLevel:        user.Vip,
		VIPExpire:       int64(user.Expire),
		SpaceTotal:      info.SpaceInfo.AllTotal.Size,
		SpaceUsed:       info.SpaceInfo.AllUse.Size,
		SpaceRemain:     info.SpaceInfo.AllRemain.Size,
		SpaceTotalText:  info.SpaceInfo.AllTotal.SizeFormat,
		SpaceUsedText:   info.SpaceInfo.AllUse.SizeFormat,
		SpaceRemainText: info.SpaceInfo.AllRemain.SizeFormat,
		UpdatedAt:       now.UTC(),
	}
	p.account = account
	p.accountAt = now
	result := *account
	return &result, nil
}
