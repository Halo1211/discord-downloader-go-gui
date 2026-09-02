package main

import (
	"log"
	"sync"
)

// downloadManager provides bounded backpressure for real-time message events.
// History jobs use their own batch workers but share this reservation map.
type downloadManager struct {
	jobs       chan downloadRequestStruct
	stop       chan struct{}
	done       chan struct{}
	workers    int
	mu         sync.Mutex
	stopOnce   sync.Once
	submitters sync.WaitGroup
	active     sync.WaitGroup
	stopped    bool
	inFlight   map[string]struct{}
}

var (
	realTimeDownloads *downloadManager
	downloadManagerMu sync.RWMutex
)

func setDownloadManager(manager *downloadManager) {
	downloadManagerMu.Lock()
	realTimeDownloads = manager
	downloadManagerMu.Unlock()
}

func currentDownloadManager() *downloadManager {
	downloadManagerMu.RLock()
	manager := realTimeDownloads
	downloadManagerMu.RUnlock()
	return manager
}

func startDownloadManager(workers, queueSize int) {
	if workers < 1 {
		workers = 1
	}
	if queueSize < workers {
		queueSize = workers
	}

	m := &downloadManager{
		jobs:     make(chan downloadRequestStruct, queueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		workers:  workers,
		inFlight: make(map[string]struct{}),
	}
	setDownloadManager(m)

	var wg sync.WaitGroup
	wg.Add(workers)
	go func() {
		wg.Wait()
		close(m.done)
	}()
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case request := <-m.jobs:
					m.run(request)
				case <-m.stop:
					for {
						select {
						case request := <-m.jobs:
							m.run(request)
						default:
							return
						}
					}
				}
			}
		}()
	}

	log.Printf("download manager started with %d worker(s) and queue size %d", workers, queueSize)
}

func downloadQueueKey(inputURL, channelID string) string {
	return channelID + "\x00" + normalizeDownloadURL(inputURL)
}

func downloadRequestChannelID(request downloadRequestStruct) string {
	channelID := "0"
	if request.Message != nil && request.Message.ChannelID != "" {
		channelID = request.Message.ChannelID
	} else if request.Channel != nil && request.Channel.ID != "" {
		channelID = request.Channel.ID
	}
	return channelID
}

func downloadRequestQueueKey(request downloadRequestStruct) string {
	return downloadQueueKey(request.InputURL, downloadRequestChannelID(request))
}

func (m *downloadManager) submit(request downloadRequestStruct) bool {
	if m == nil {
		return false
	}
	key := downloadRequestQueueKey(request)
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return false
	}
	m.submitters.Add(1)
	if _, exists := m.inFlight[key]; exists {
		m.submitters.Done()
		m.mu.Unlock()
		return true
	}
	m.inFlight[key] = struct{}{}
	m.active.Add(1)
	m.mu.Unlock()
	defer m.submitters.Done()

	m.jobs <- request
	return true
}

func (m *downloadManager) release(key string) {
	m.mu.Lock()
	_, existed := m.inFlight[key]
	if existed {
		delete(m.inFlight, key)
	}
	m.mu.Unlock()
	if existed {
		m.active.Done()
	}
}

func (m *downloadManager) reserve(inputURL, channelID string) bool {
	if m == nil {
		return true
	}
	key := downloadQueueKey(inputURL, channelID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return false
	}
	if _, exists := m.inFlight[key]; exists {
		return false
	}
	m.inFlight[key] = struct{}{}
	m.active.Add(1)
	return true
}

func runHistoryDownload(request downloadRequestStruct) (downloadStatusStruct, int64) {
	manager := currentDownloadManager()
	if manager == nil {
		return request.handleDownload()
	}
	key := downloadRequestQueueKey(request)
	if !manager.reserve(request.InputURL, downloadRequestChannelID(request)) {
		return mDownloadStatus(downloadSkippedDuplicate), 0
	}
	defer manager.release(key)
	return request.handleDownload()
}

func (m *downloadManager) run(request downloadRequestStruct) {
	key := downloadRequestQueueKey(request)
	defer m.release(key)
	request.handleDownload()
}

func (m *downloadManager) stopAndWait() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if !m.stopped {
		m.stopped = true
	}
	m.mu.Unlock()
	m.submitters.Wait()
	m.stopOnce.Do(func() { close(m.stop) })
	<-m.done
	m.active.Wait()
}

func (m *downloadManager) stats() (workers, queued, inFlight int, stopped bool) {
	if m == nil {
		return 0, 0, 0, true
	}
	m.mu.Lock()
	workers = m.workers
	queued = len(m.jobs)
	inFlight = len(m.inFlight)
	stopped = m.stopped
	m.mu.Unlock()
	return
}

func queueRealTimeDownload(request downloadRequestStruct) bool {
	manager := currentDownloadManager()
	if manager == nil {
		return false
	}
	return manager.submit(request)
}

func downloadIsInFlight(inputURL, channelID string) bool {
	manager := currentDownloadManager()
	if manager == nil {
		return false
	}
	key := downloadQueueKey(inputURL, channelID)
	manager.mu.Lock()
	_, exists := manager.inFlight[key]
	manager.mu.Unlock()
	return exists
}
