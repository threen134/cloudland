package apis

import (
	"sync"
	"testing"
	"time"
)

// Concurrent attempts must not pass the limit together: the attempt is counted before the password check,
// which takes about 100ms, so requests overlapping it all have to see each other.
func TestReserveHostConsoleAttemptConcurrent(t *testing.T) {
	const userID = 4242
	defer clearHostConsoleAttempts(userID)

	now := time.Now()
	allowed := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reserveHostConsoleAttempt(userID, now) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != hostConsoleMaxFailures {
		t.Errorf("allowed %d attempts, want %d", allowed, hostConsoleMaxFailures)
	}
	if reserveHostConsoleAttempt(userID, now) {
		t.Error("an attempt over the limit should be refused")
	}
	// Attempts older than the window are forgotten
	if !reserveHostConsoleAttempt(userID, now.Add(hostConsoleFailureWindow+time.Second)) {
		t.Error("attempts outside the window should be forgotten")
	}
	// A correct password clears the count
	clearHostConsoleAttempts(userID)
	for i := 0; i < hostConsoleMaxFailures; i++ {
		if !reserveHostConsoleAttempt(userID, now) {
			t.Fatalf("attempt %d should be allowed after clearing", i)
		}
	}
}
