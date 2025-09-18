# mutatelock
Upgradeable RWLock

While many goroutines can hold standard read-locks with this library, only one "special" goroutine may have a read-lock that is upgradeable to a read/write lock.
