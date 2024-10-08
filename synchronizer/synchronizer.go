package synchronizer

import (
	"context"
	"github.com/OAB/pool"
	stateTypes "github.com/OAB/state/types"
	"github.com/OAB/synchronizer/types"
	"github.com/OAB/utils/chains"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"sync"
	"time"
)

type Synchronizer struct {
	pool  types.PoolInterface
	ether types.EthereumInterface
	state types.StateInterface
	cfg   Config

	logger    log.Logger
	ctx       context.Context
	cancelCtx context.CancelFunc
}

func NewSynchronizer(pool types.PoolInterface,
	ethereum types.EthereumInterface,
	state types.StateInterface,
	cfg Config) (*Synchronizer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &Synchronizer{
		pool:      pool,
		ether:     ethereum,
		state:     state,
		cfg:       cfg,
		logger:    log.New("service", "synchronizer"),
		ctx:       ctx,
		cancelCtx: cancel,
	}, nil
}

func (s *Synchronizer) Start() {
	s.sync()
}

func (s *Synchronizer) Stop() {
	s.logger.Info("Sync stop")
	s.cancelCtx()
}

func (s *Synchronizer) sync() {
	s.logger.Info("Sync start")
	go s.syncTickets()
	go s.syncAccountCreated()
}

func (s *Synchronizer) syncTickets() {
	s.logger.Info("Components 1/2", "component", "tickets")
	// start all chains tickets sync
	var chans []<-chan pool.TicketFull

	for _, network := range s.cfg.EthereumCfg.Networks {
		ch := make(chan pool.TicketFull)
		chans = append(chans, ch)
		go func(chainId chains.ChainId, ch chan pool.TicketFull) {
			if err := s.ether.WatchEntryPointEvent(s.ctx, chainId, 0, ch); err != nil {
				s.logger.Error("Failed to start event listener", "chainId", chainId, "error", err)
			}
		}(chains.ChainId(network.ChainId), ch)
	}

	// mock
	go func() {
		ch := make(chan pool.TicketFull)
		chans = append(chans, ch)
		time.Sleep(3 * time.Second)
		insertTicket(ch)
	}()
	time.Sleep(1 * time.Second)

	ticketChannel := mergeChannels(s.ctx, chans...)

	for {
		select {
		case ticket, ok := <-ticketChannel:
			if !ok {
				return
			}
			s.logger.Info("Synchronize to a new ticket")
			// check
			//err := s.state.AddTicket(ticket)
			//if err != nil {
			//	s.logger.Warn("Failed to synchronize to a new ticket", "error", err)
			//}
			s.pool.AddTicket(ticket)
		case <-s.ctx.Done():
			s.logger.Warn("Stopping Sync due to context cancellation")
			return
		default:
		}
	}
}

func (s *Synchronizer) syncAccountCreated() {
	s.logger.Info("Components 2/2", "component", "accountCreated")
	ch := make(chan stateTypes.AccountMapping)

	go func(ch chan stateTypes.AccountMapping) {
		if err := s.ether.WatchAAFactoryEvent(s.ctx, 0, ch); err != nil {
			s.logger.Error("Failed to start event listener", "error", err)
		}
	}(ch)

	go func() {
		// get all events
		time.Sleep(3 * time.Second)
		mappingInsert := stateTypes.AccountMapping{
			User:    common.HexToAddress("27916984c665f15041929B68451303136FA16653"),
			Account: common.HexToAddress("D31959035048676fc27d84C8Bc120997204b16B6"),
		}
		ch <- mappingInsert
	}()

	for {
		select {
		case mapping, ok := <-ch:
			if !ok {
				return
			}
			s.logger.Info("Synchronize to a new account mapping", "user", mapping.User, "account", mapping.Account)
			err := s.state.AddNewMapping(mapping)
			if err != nil {
				s.logger.Warn("Add a new account mapping error", "error", err)
			}
		case <-s.ctx.Done():
			s.logger.Warn("Stopping Sync due to context cancellation")
			return
		default:
		}
	}
}

func mergeChannels(ctx context.Context, chans ...<-chan pool.TicketFull) <-chan pool.TicketFull {
	out := make(chan pool.TicketFull)
	var wg sync.WaitGroup
	wg.Add(len(chans))

	for _, ch := range chans {
		go func(c <-chan pool.TicketFull) {
			defer wg.Done()
			for {
				select {
				case ticket, ok := <-c:
					if !ok {
						return
					}
					select {
					case out <- ticket:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(ch)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}
