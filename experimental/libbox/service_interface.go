package libbox

import (
	"context"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing/service/pause"
)

func NewBoxService(ctx context.Context, cancel context.CancelFunc, instance *box.Box, urlTestHistoryStorage *urltest.HistoryStorage, pauseManager pause.Manager, clashServer adapter.ClashServer) BoxService {
	return BoxService{
		ctx:                   ctx,
		cancel:                cancel,
		instance:              instance,
		pauseManager:          pauseManager,
		urlTestHistoryStorage: urlTestHistoryStorage,
		clashServer:           clashServer,
	}
}

func (b *BoxService) GetInstance() *box.Box {
	return b.instance
}

func (b *BoxService) GetContext() context.Context {
	return b.ctx
}
