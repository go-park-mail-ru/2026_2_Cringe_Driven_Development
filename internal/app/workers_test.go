package app

import (
	"context"
	"testing"
	"time"
)

// blockingWorker позволяет тесту отдельно управлять запуском и завершением фонового обработчика.
type blockingWorker struct{ started, release, finished chan struct{} }

// Run сигнализирует о запуске и ждёт разрешения теста на завершение.
func (w blockingWorker) Run(ctx context.Context) error {
	close(w.started)
	<-ctx.Done()
	<-w.release
	close(w.finished)
	return nil
}

// TestApplicationWaitsForBothWorkersBeforeClosingPool проверяет, что приложение дожидается обоих
// обработчиков до закрытия общих ресурсов.
func TestApplicationWaitsForBothWorkersBeforeClosingPool(t *testing.T) {
	a := blockingWorker{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	b := blockingWorker{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	stop := startWorkers(context.Background(), a, b)
	<-a.started
	<-b.started
	resourcesClosed := make(chan struct{})
	go func() { stop(); close(resourcesClosed) }()
	close(a.release)
	<-a.finished
	select {
	case <-resourcesClosed:
		t.Fatal("resources closed while second worker still running")
	default:
	}
	close(b.release)
	select {
	case <-resourcesClosed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
}
