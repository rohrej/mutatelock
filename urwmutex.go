// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Modified by Dr. Justin P. Rohrer, Naval Postgraduate School, 2020
// Adds upgrade/downgrade functionality

package mutatelock

import (
	"log"
	"sync"
	"sync/atomic"
	_ "unsafe"
)

// A URWMutex is a reader/writer mutual exclusion lock that may be upgraded from a read-lock to a write-lock.
// The lock can be held by an arbitrary number of readers or a single writer.
// The zero value for a URWMutex is an unlocked mutex.
//
// A URWMutex must not be copied after first use.
//
// If any goroutine calls Lock while the lock is already held by
// one or more readers, concurrent calls to RLock will block until
// the writer has acquired (and released) the lock, to ensure that
// the lock eventually becomes available to the writer.
// Note that this prohibits recursive read-locking.
//
// In the terminology of the Go memory model,
// the n'th call to Unlock “synchronizes before” the m'th call to Lock
// for any n < m, just as for Mutex.
// For any call to RLock, there exists an n such that
// the n'th call to Unlock “synchronizes before” that call to RLock,
// and the corresponding call to RUnlock “synchronizes before”
// the n+1'th call to Lock.
type URWMutex struct {
	w           sync.Mutex   // held if there are pending writers
	writerSem   uint32       // semaphore for writers to wait for completing readers
	readerSem   uint32       // semaphore for readers to wait for completing writers
	readerCount atomic.Int32 // number of pending readers
	readerWait  atomic.Int32 // number of departing readers
	ugAble      atomic.Bool  // set if this instance holds an upgradable read-lock
	ug          atomic.Bool  // set if this instance was upgraded from read to write
}

const urwmutexMaxReaders = 1 << 30

//go:linkname semacquireRWMutexR sync.runtime_SemacquireRWMutexR
func semacquireRWMutexR(s *uint32, handoff bool, skipframes int)

//go:linkname semRelease sync.runtime_Semrelease
func semRelease(s *uint32, handoff bool, skipframes int)

// Happens-before relationships are indicated to the race detector via:
// - Unlock  -> Lock:  readerSem
// - Unlock  -> RLock: readerSem
// - RUnlock -> Lock:  writerSem
//
// The methods below temporarily disable handling of race synchronization
// events in order to provide the more precise model above to the race
// detector.
//
// For example, atomic.AddInt32 in RLock should not appear to provide
// acquire-release semantics, which would incorrectly synchronize racing
// readers, thus potentially missing races.

// RLock locks rw for reading.
//
// It should not be used for recursive read locking; a blocked Lock
// call excludes new readers from acquiring the lock. See the
// documentation on the URWMutex type.
func (rw *URWMutex) RLock() {
	if rw.readerCount.Add(1) < 0 {
		// A writer is pending, wait for it.
		semacquireRWMutexR(&rw.readerSem, false, 0)
	}
}

// TryRLock tries to lock rw for reading and reports whether it succeeded.
//
// Note that while correct uses of TryRLock do exist, they are rare,
// and use of TryRLock is often a sign of a deeper problem
// in a particular use of mutexes.
func (rw *URWMutex) TryRLock() bool {
	for {
		c := rw.readerCount.Load()
		if c < 0 {
			return false
		}
		if rw.readerCount.CompareAndSwap(c, c+1) {
			return true
		}
	}
}

// URLock locks rw for writing, but continues to allow readers while not upgraded.
//
// It should not be used for recursive read locking; a blocked Lock
// call excludes new readers from acquiring the lock. See the
// documentation on the URWMutex type.
func (rw *URWMutex) URLock() {
	// First, resolve competition with other writers.
	// Take write lock, but continue to allow other readers
	rw.w.Lock()
	rw.ugAble.Store(true)
}

// RUnlock undoes a single RLock call;
// it does not affect other simultaneous readers.
// It is a run-time error if rw is not locked for reading
// on entry to RUnlock.
func (rw *URWMutex) RUnlock() {
	if r := rw.readerCount.Add(-1); r < 0 {
		// Outlined slow-path to allow the fast-path to be inlined
		rw.rUnlockSlow(r)
	}
}

func (rw *URWMutex) rUnlockSlow(r int32) {
	if r+1 == 0 || r+1 == -urwmutexMaxReaders {
		log.Fatal("urwmutex: RUnlock of unlocked URWMutex")
	}
	// A writer is pending.
	if rw.readerWait.Add(-1) == 0 {
		// The last reader unblocks the writer.
		semRelease(&rw.writerSem, false, 1)
	}
}

// URUnlock undoes a single RRLock call, whether or not it has been upgraded;
// It is a run-time error if rw is not locked for reading
// on entry to URUnlock.
func (rw *URWMutex) URUnlock() {
	rw.ugAble.Store(false)
	if rw.ug.Swap(false) {
		rw.Unlock()
	} else {
		rw.w.Unlock()
	}
}

// Lock locks rw for writing.
// If the lock is already locked for reading or writing,
// Lock blocks until the lock is available.
func (rw *URWMutex) Lock() {
	// First, resolve competition with other writers.
	rw.w.Lock()
	// Announce to readers there is a pending writer.
	r := rw.readerCount.Add(-urwmutexMaxReaders) + urwmutexMaxReaders
	// Wait for active readers.
	if r != 0 && rw.readerWait.Add(r) != 0 {
		semacquireRWMutexR(&rw.writerSem, false, 0)
	}
}

// TryLock tries to lock rw for writing and reports whether it succeeded.
//
// Note that while correct uses of TryLock do exist, they are rare,
// and use of TryLock is often a sign of a deeper problem
// in a particular use of mutexes.
func (rw *URWMutex) TryLock() bool {
	if !rw.w.TryLock() {
		return false
	}
	if !rw.readerCount.CompareAndSwap(0, -urwmutexMaxReaders) {
		rw.w.Unlock()
		return false
	}
	return true
}

// UpgradeLock upgrades an existing upgradeable read lock to a write lock
// If the lock is already locked for reading or writing,
// UpgradeLock blocks until the lock is available.
func (rw *URWMutex) UpgradeLock() {
	if !rw.ugAble.Load() {
		log.Fatal("urwmutex: Upgrade lock not available")
	}
	rw.ug.Store(true)
	// Announce to readers there is a pending writer.
	r := rw.readerCount.Add(-urwmutexMaxReaders) + urwmutexMaxReaders
	// Wait for active readers.
	if r != 0 && rw.readerWait.Add(r) != 0 {
		semacquireRWMutexR(&rw.readerSem, false, 0)
	}
}

// DowngradeLock unlocks rw for writing. It is a run-time error if rw is
// not an upgraded lock or is not locked for writing on entry to DowngradeLock.
//
// As with Mutexes, a locked URWMutex is not associated with a particular
// goroutine. One goroutine may UpgradeLock (Lock) a URWMutex and then
// arrange for another goroutine to DowngradeLock (Unlock) it.
func (rw *URWMutex) DowngradeLock() {
	if !rw.ug.Load() {
		log.Fatal("urwmutex: Downgrade lock not available")
	}
	// Announce to readers there is no active writer.
	r := rw.readerCount.Add(urwmutexMaxReaders)
	if r >= urwmutexMaxReaders {
		log.Fatal("urwmutex: Unlock of unlocked URWMutex")
	}
	// Unblock blocked readers, if any.
	for i := 0; i < int(r); i++ {
		semRelease(&rw.readerSem, false, 0)
	}
	rw.ug.Store(false)
}

// Unlock unlocks rw for writing. It is a run-time error if rw is
// not locked for writing on entry to Unlock.
//
// As with Mutexes, a locked URWMutex is not associated with a particular
// goroutine. One goroutine may RLock (Lock) a URWMutex and then
// arrange for another goroutine to RUnlock (Unlock) it.
func (rw *URWMutex) Unlock() {
	// Announce to readers there is no active writer.
	r := rw.readerCount.Add(urwmutexMaxReaders)
	if r >= urwmutexMaxReaders {
		log.Fatal("urwmutex: Unlock of unlocked URWMutex")
	}
	// Unblock blocked readers, if any.
	for i := 0; i < int(r); i++ {
		semRelease(&rw.readerSem, false, 0)
	}
	// Allow other writers to proceed.
	rw.w.Unlock()
}

// RLocker returns a Locker interface that implements
// the Lock and Unlock methods by calling rw.RLock and rw.RUnlock.
func (rw *URWMutex) RLocker() sync.Locker {
	return (*rlocker)(rw)
}

type rlocker URWMutex

func (r *rlocker) Lock()   { (*URWMutex)(r).RLock() }
func (r *rlocker) Unlock() { (*URWMutex)(r).RUnlock() }
