// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Modified by Dr. Justin P. Rohrer, Naval Postgraduate School, 2020
// Tests upgrade/downgrade functionality of URWmutex

// GOMAXPROCS=10 go test

package mutatelock

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func parallelReader(m *URWMutex, clocked, cunlock, cdone chan bool) {
	m.RLock()
	clocked <- true
	<-cunlock
	m.RUnlock()
	cdone <- true
}

func doTestParallelReaders(numReaders, gomaxprocs int) {
	runtime.GOMAXPROCS(gomaxprocs)
	var m URWMutex
	clocked := make(chan bool)
	cunlock := make(chan bool)
	cdone := make(chan bool)
	for i := 0; i < numReaders; i++ {
		go parallelReader(&m, clocked, cunlock, cdone)
	}
	// Wait for all parallel RLock()s to succeed.
	for i := 0; i < numReaders; i++ {
		<-clocked
	}
	for i := 0; i < numReaders; i++ {
		cunlock <- true
	}
	// Wait for the goroutines to finish.
	for i := 0; i < numReaders; i++ {
		<-cdone
	}
}

func TestParallelReaders(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(-1))
	doTestParallelReaders(1, 4)
	doTestParallelReaders(3, 4)
	doTestParallelReaders(4, 2)
}

func reader(rwm *URWMutex, num_iterations int, activity *int32, cdone chan bool) {
	for i := 0; i < num_iterations; i++ {
		rwm.RLock()
		n := atomic.AddInt32(activity, 1)
		if n < 1 || n >= 10000 {
			rwm.RUnlock()
			panic(fmt.Sprintf("wlock(%d)\n", n))
		}
		for i := 0; i < 100; i++ {
		}
		atomic.AddInt32(activity, -1)
		rwm.RUnlock()
	}
	cdone <- true
}

func writer(rwm *URWMutex, num_iterations int, activity *int32, cdone chan bool) {
	for i := 0; i < num_iterations; i++ {
		rwm.Lock()
		n := atomic.AddInt32(activity, 10000)
		if n != 10000 {
			rwm.Unlock()
			panic(fmt.Sprintf("wlock(%d)\n", n))
		}
		for i := 0; i < 100; i++ {
		}
		atomic.AddInt32(activity, -10000)
		rwm.Unlock()
	}
	cdone <- true
}

func HammerRWMutex(gomaxprocs, numReaders, num_iterations int) {
	runtime.GOMAXPROCS(gomaxprocs)
	// Number of active readers + 10000 * number of active writers.
	var activity int32
	var rwm URWMutex
	cdone := make(chan bool)
	go writer(&rwm, num_iterations, &activity, cdone)
	var i int
	for i = 0; i < numReaders/2; i++ {
		go reader(&rwm, num_iterations, &activity, cdone)
	}
	go writer(&rwm, num_iterations, &activity, cdone)
	for ; i < numReaders; i++ {
		go reader(&rwm, num_iterations, &activity, cdone)
	}
	// Wait for the 2 writers and all readers to finish.
	for i := 0; i < 2+numReaders; i++ {
		<-cdone
	}
}

func TestRWMutex(t *testing.T) {
	var m URWMutex

	m.Lock()
	if m.TryLock() {
		t.Fatalf("TryLock succeeded with mutex locked")
	}
	if m.TryRLock() {
		t.Fatalf("TryRLock succeeded with mutex locked")
	}
	m.Unlock()

	if !m.TryLock() {
		t.Fatalf("TryLock failed with mutex unlocked")
	}
	m.Unlock()

	if !m.TryRLock() {
		t.Fatalf("TryRLock failed with mutex unlocked")
	}
	if !m.TryRLock() {
		t.Fatalf("TryRLock failed with mutex rlocked")
	}
	if m.TryLock() {
		t.Fatalf("TryLock succeeded with mutex rlocked")
	}
	m.RUnlock()
	m.RUnlock()

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(-1))
	n := 1000
	if testing.Short() {
		n = 5
	}
	HammerRWMutex(1, 1, n)
	HammerRWMutex(1, 3, n)
	HammerRWMutex(1, 10, n)
	HammerRWMutex(4, 1, n)
	HammerRWMutex(4, 3, n)
	HammerRWMutex(4, 10, n)
	HammerRWMutex(10, 1, n)
	HammerRWMutex(10, 3, n)
	HammerRWMutex(10, 10, n)
	HammerRWMutex(10, 5, n)
}

func TestRLocker(t *testing.T) {
	var wl URWMutex
	var rl sync.Locker
	wlocked := make(chan bool, 1)
	rlocked := make(chan bool, 1)
	rl = wl.RLocker()
	n := 10
	go func() {
		for i := 0; i < n; i++ {
			rl.Lock()
			rl.Lock()
			rlocked <- true
			wl.Lock()
			wlocked <- true
		}
	}()
	for i := 0; i < n; i++ {
		<-rlocked
		rl.Unlock()
		select {
		case <-wlocked:
			t.Fatal("RLocker() didn't read-lock it")
		default:
		}
		rl.Unlock()
		<-wlocked
		select {
		case <-rlocked:
			t.Fatal("RLocker() didn't respect the write lock")
		default:
		}
		wl.Unlock()
	}
}

func BenchmarkRWMutexUncontended(b *testing.B) {
	type PaddedRWMutex struct {
		URWMutex
		pad [32]uint32
	}
	b.RunParallel(func(pb *testing.PB) {
		var rwm PaddedRWMutex
		for pb.Next() {
			rwm.RLock()
			rwm.RLock()
			rwm.RUnlock()
			rwm.RUnlock()
			rwm.Lock()
			rwm.Unlock()
		}
	})
}

func benchmarkRWMutex(b *testing.B, localWork, writeRatio int) {
	var rwm URWMutex
	b.RunParallel(func(pb *testing.PB) {
		foo := 0
		for pb.Next() {
			foo++
			if foo%writeRatio == 0 {
				rwm.Lock()
				rwm.Unlock()
			} else {
				rwm.RLock()
				for i := 0; i != localWork; i += 1 {
					foo *= 2
					foo /= 2
				}
				rwm.RUnlock()
			}
		}
		_ = foo
	})
}

func BenchmarkRWMutexWrite100(b *testing.B) {
	benchmarkRWMutex(b, 0, 100)
}

func BenchmarkRWMutexWrite10(b *testing.B) {
	benchmarkRWMutex(b, 0, 10)
}

func BenchmarkRWMutexWorkWrite100(b *testing.B) {
	benchmarkRWMutex(b, 100, 100)
}

func BenchmarkRWMutexWorkWrite10(b *testing.B) {
	benchmarkRWMutex(b, 100, 10)
}

func Test_UWL_Block_Read(t *testing.T) {
	m := URWMutex{}
	m.Lock()
	i := atomic.Int32{}
	go func() {
		m.URLock()
		defer m.URUnlock()
		i.Add(1)
	}()
	time.Sleep(time.Millisecond * 10)
	assert.Equal(t, int32(0), i.Load())
	m.Unlock()
}

func Test_UWL_Block_Write(t *testing.T) {
	m := URWMutex{}
	m.URLock()

	m.UpgradeLock()
	i := atomic.Int32{}
	go func() {
		m.Lock()
		defer m.Unlock()
		i.Add(1)
	}()
	time.Sleep(time.Millisecond * 10)
	assert.Equal(t, int32(0), i.Load())
	m.URUnlock()
}

func Test_URL_Allow(t *testing.T) {
	m := URWMutex{}
	m.URLock()

	waitGroup := sync.WaitGroup{}
	for i := 0; i < 10; i++ {
		waitGroup.Add(1)
		go func() {
			m.RLock()
			defer m.RLock()
			waitGroup.Done()
		}()
	}
	waitGroup.Wait()
	m.URUnlock()
}

func Test_UUL_Block_Read(t *testing.T) {
	m := URWMutex{}
	m.URLock()

	m.UpgradeLock()
	i := atomic.Int32{}
	go func() {
		m.RLock()
		defer m.RUnlock()
		i.Add(1)
	}()
	time.Sleep(time.Millisecond * 10)
	assert.Equal(t, int32(0), i.Load())
	m.URUnlock()
}

func Test_UDL_Allow_Read(t *testing.T) {
	m := URWMutex{}
	m.URLock()

	m.UpgradeLock()
	i := atomic.Int32{}
	go func() {
		m.RLock()
		defer m.RUnlock()
		i.Add(1)
	}()
	m.DowngradeLock()
	time.Sleep(time.Millisecond * 10)
	assert.Equal(t, int32(1), i.Load())
	m.URUnlock()
}

func Test_URL_Allow_Read(t *testing.T) {
	m := URWMutex{}

	m.RLock()
	defer m.RUnlock()

	m.RLock()
	defer m.RUnlock()

	m.URLock()
	defer m.URUnlock()
}

func TestUUL_Block_Upgradeable(t *testing.T) {
	m := URWMutex{}

	m.RLock()
	defer m.RUnlock()

	m.RLock()
	defer m.RUnlock()

	m.URLock()
	i := atomic.Int32{}
	go func() {
		m.URLock()
		defer m.URUnlock()
		i.Add(1)
	}()
	time.Sleep(time.Millisecond * 10)
	assert.Equal(t, int32(0), i.Load())
	m.URUnlock()
}
