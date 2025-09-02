package cache

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

const (
	testString     = "test"
	benchmarkValue = "benchmark value"
)

func TestCache_SetAndGet(t *testing.T) {
	t.Parallel()

	cache := New[string]()

	value := "test value"
	cache.Set("key1", &value, 0)

	got, found := cache.Get("key1")
	if !found {
		t.Fatal("expected to find key1")
	}

	if *got != value {
		t.Errorf("expected %q, got %q", value, *got)
	}
}

func TestCache_GetNonExistent(t *testing.T) {
	t.Parallel()

	cache := New[string]()

	_, found := cache.Get("nonexistent")
	if found {
		t.Error("expected not to find nonexistent key")
	}
}

func TestCache_TTLExpiration(t *testing.T) {
	cache := New[string]()

	value := "expires soon"
	cache.Set("key1", &value, 100*time.Millisecond)

	// Should exist immediately
	if _, found := cache.Get("key1"); !found {
		t.Error("expected to find key1 immediately after setting")
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should not exist after expiration
	if _, found := cache.Get("key1"); found {
		t.Error("expected key1 to be expired")
	}
}

func TestCache_NoTTL(t *testing.T) {
	cache := New[string]()

	value := "never expires"
	cache.Set("key1", &value, 0)

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Should still exist
	if _, found := cache.Get("key1"); !found {
		t.Error("expected key1 to still exist (no TTL)")
	}
}

func TestCache_WeakReference(t *testing.T) {
	t.Parallel()

	cache := New[string]()

	// Create a value in a limited scope
	func() {
		value := "will be garbage collected"
		cache.Set("key1", &value, 0)
	}()

	// Force garbage collection
	runtime.GC()
	runtime.GC() // Call twice to ensure collection

	// Give GC time to run
	time.Sleep(50 * time.Millisecond)

	// The value might or might not be GC'd depending on runtime behavior
	// This test demonstrates the weak reference, but GC timing is non-deterministic
	_, found := cache.Get("key1")
	t.Logf("After GC, key found: %v (non-deterministic)", found)
}

func TestCache_Delete(t *testing.T) {
	t.Parallel()

	cache := New[string]()

	value := "to be deleted"
	cache.Set("key1", &value, 0)

	if _, found := cache.Get("key1"); !found {
		t.Fatal("expected to find key1 before deletion")
	}

	cache.Delete("key1")

	if _, found := cache.Get("key1"); found {
		t.Error("expected key1 to be deleted")
	}
}

func TestCache_Clear(t *testing.T) {
	t.Parallel()

	cache := New[string]()

	val1, val2, val3 := "value1", "value2", "value3"
	cache.Set("key1", &val1, 0)
	cache.Set("key2", &val2, 0)
	cache.Set("key3", &val3, 0)

	if cache.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", cache.Len())
	}

	cache.Clear()

	if cache.Len() != 0 {
		t.Errorf("expected 0 entries after clear, got %d", cache.Len())
	}

	if _, found := cache.Get("key1"); found {
		t.Error("expected all keys to be cleared")
	}
}

func TestCache_Cleanup(t *testing.T) {
	cache := New[string]()

	// Add some expired entries
	val1 := "expired1"
	cache.Set("expired1", &val1, 50*time.Millisecond)

	val2 := "expired2"
	cache.Set("expired2", &val2, 50*time.Millisecond)

	// Add a non-expired entry
	val3 := "valid"
	cache.Set("valid", &val3, 0)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Run cleanup
	removed := cache.Cleanup()

	if removed != 2 {
		t.Errorf("expected to remove 2 expired entries, removed %d", removed)
	}

	if cache.Len() != 1 {
		t.Errorf("expected 1 entry remaining, got %d", cache.Len())
	}

	if _, found := cache.Get("valid"); !found {
		t.Error("expected valid entry to still exist")
	}
}

func TestCache_CleanupTimer(t *testing.T) {
	cache := New[string]()

	// Start cleanup timer with short interval
	stop := cache.StartCleanupTimer(50 * time.Millisecond)
	defer stop()

	// Add expired entries
	val1 := "expires"
	cache.Set("key1", &val1, 30*time.Millisecond)

	// Wait for cleanup to run
	time.Sleep(150 * time.Millisecond)

	// Entry should be cleaned up
	if _, found := cache.Get("key1"); found {
		t.Error("expected cleanup timer to remove expired entry")
	}
}

func TestCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	cache := New[int]()

	var waitGroup sync.WaitGroup

	numGoroutines := 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		waitGroup.Add(1)

		go func(id int) {
			defer waitGroup.Done()

			value := id
			cache.Set("key", &value, 0)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			cache.Get("key")
		}()
	}

	// Concurrent deletes
	for i := 0; i < numGoroutines; i++ {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			cache.Delete("key")
		}()
	}

	waitGroup.Wait()

	// No panic means success
	t.Log("Concurrent access test passed without panic")
}

func TestCache_MultipleTypes(t *testing.T) {
	t.Parallel()

	// String cache
	stringCache := New[string]()
	str := testString
	stringCache.Set("key", &str, 0)

	if val, found := stringCache.Get("key"); !found || *val != testString {
		t.Error("string cache failed")
	}

	// Int cache
	intCache := New[int]()
	num := 42
	intCache.Set("key", &num, 0)

	if val, found := intCache.Get("key"); !found || *val != 42 {
		t.Error("int cache failed")
	}

	// Struct cache
	type MyStruct struct {
		Field1 string
		Field2 int
	}

	structCache := New[MyStruct]()
	structVal := MyStruct{Field1: "test", Field2: 123}
	structCache.Set("key", &structVal, 0)

	if val, found := structCache.Get("key"); !found || val.Field1 != "test" || val.Field2 != 123 {
		t.Error("struct cache failed")
	}
}

func TestCache_Len(t *testing.T) {
	t.Parallel()

	cache := New[string]()

	if cache.Len() != 0 {
		t.Errorf("expected empty cache to have length 0, got %d", cache.Len())
	}

	val1, val2 := "value1", "value2"
	cache.Set("key1", &val1, 0)
	cache.Set("key2", &val2, 0)

	if cache.Len() != 2 {
		t.Errorf("expected length 2, got %d", cache.Len())
	}

	cache.Delete("key1")

	if cache.Len() != 1 {
		t.Errorf("expected length 1 after delete, got %d", cache.Len())
	}
}

func BenchmarkCache_Set(benchContext *testing.B) {
	cache := New[string]()
	value := benchmarkValue

	benchContext.ResetTimer()

	for index := 0; index < benchContext.N; index++ {
		cache.Set("key", &value, 0)
	}
}

func BenchmarkCache_Get(benchContext *testing.B) {
	cache := New[string]()
	value := benchmarkValue
	cache.Set("key", &value, 0)

	benchContext.ResetTimer()

	for index := 0; index < benchContext.N; index++ {
		cache.Get("key")
	}
}

func BenchmarkCache_SetWithTTL(benchContext *testing.B) {
	cache := New[string]()
	value := benchmarkValue

	benchContext.ResetTimer()

	for index := 0; index < benchContext.N; index++ {
		cache.Set("key", &value, 5*time.Minute)
	}
}

func BenchmarkCache_ConcurrentReads(benchContext *testing.B) {
	cache := New[string]()
	value := benchmarkValue
	cache.Set("key", &value, 0)

	benchContext.ResetTimer()
	benchContext.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("key")
		}
	})
}

func BenchmarkCache_ConcurrentWrites(benchContext *testing.B) {
	cache := New[string]()
	value := benchmarkValue

	benchContext.ResetTimer()
	benchContext.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Set("key", &value, 0)
		}
	})
}
