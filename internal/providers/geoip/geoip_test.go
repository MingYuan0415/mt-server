package geoip

import (
	"encoding/binary"
	"io"
	"log/slog"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oschwald/geoip2-golang"
)

const testWaitTimeout = 5 * time.Second

func TestResolveReturnsNormalizedDisplaySafeLocation(t *testing.T) {
	path := writeFixture(t, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5431, 114.0579, 50),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	resolved, err := store.Resolve(netip.MustParseAddr("8.8.8.8"))
	if err != nil {
		t.Fatal(err)
	}
	point := resolved.Point
	if point.Latitude != 22.54 || point.Longitude != 114.06 ||
		point.City != "Shenzhen" || point.Region != "Guangdong" ||
		point.Country != "CN" || point.Timezone != "Asia/Shanghai" ||
		point.Source != "ip" || point.Provider != "maxmind" || point.Precision != "coarse" {
		t.Fatalf("unexpected resolved point %#v", point)
	}
	if resolved.AccuracyKm == nil || *resolved.AccuracyKm != 50 {
		t.Fatalf("unexpected accuracy %#v", resolved.AccuracyKm)
	}
}

func TestResolveRejectsNonPublicAddresses(t *testing.T) {
	path := writeFixture(t, map[netip.Prefix][]byte{
		netip.MustParsePrefix("192.0.2.0/24"): mmdbCity("Docs", "", "", "US", "United States",
			"Etc/UTC", 30, 90, 10),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	for _, address := range []string{
		"192.168.1.1", "127.0.0.1", "10.0.0.5", "100.64.0.1", "169.254.1.1",
		"0.1.2.3", "192.0.2.1", "203.0.113.7", "198.51.100.4", "198.18.1.1",
		"198.19.1.1", "192.88.99.1", "192.31.196.1", "192.52.193.1",
		"192.175.48.1", "240.0.0.1", "241.0.0.1", "255.255.255.255",
		"::1", "2001:db8::1", "2001:1::1", "2002::1", "64:ff9b::1",
		"64:ff9b:1::1", "100::1", "100:0:0:1::1", "2620:4f:8000::1",
		"3fff::1", "5f00::1", "fe80::1",
		"::ffff:192.168.1.1",
	} {
		if _, err := store.Resolve(netip.MustParseAddr(address)); err != ErrNotPublic {
			t.Errorf("address %s: expected ErrNotPublic, got %v", address, err)
		}
	}
}

func TestResolveAcceptsGlobalUnicast(t *testing.T) {
	path := writeFixture(t, map[netip.Prefix][]byte{
		netip.MustParsePrefix("1.1.1.0/24"): mmdbCity("City", "Region", "RG", "US", "United States",
			"America/Los_Angeles", 33, -117, 20),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	if _, err := store.Resolve(netip.MustParseAddr("1.1.1.1")); err != nil {
		t.Fatalf("global unicast IPv4 should resolve, got %v", err)
	}
	if _, err := store.Resolve(netip.MustParseAddr("2606:4700:4700::1111")); err != ErrNotFound {
		t.Fatalf("IPv6 in an IPv4-only database should be not found, got %v", err)
	}
}

func TestResolveNotFoundWhenDatabaseHasNoRecord(t *testing.T) {
	path := writeFixture(t, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	for _, address := range []string{"9.9.9.9", "8.8.9.9"} {
		if _, err := store.Resolve(netip.MustParseAddr(address)); err != ErrNotFound {
			t.Errorf("address %s: expected ErrNotFound, got %v", address, err)
		}
	}
	resolved, err := store.Resolve(netip.MustParseAddr("8.8.8.255"))
	if err != nil || resolved.Point.City != "Shenzhen" {
		t.Fatalf("last /24 address should resolve: %#v %v", resolved, err)
	}
}

func TestResolveNotFoundForCountryOnlyRecord(t *testing.T) {
	path := writeFixture(t, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCountryOnly("CN"),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for country-only record, got %v", err)
	}
}

func TestStoreRejectsNonCityDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-Country.mmdb")
	writeMMDBWithType(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCountryOnly("CN"),
	}, "GeoLite2-Country")
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable for a Country database, got %v", err)
	}
}

func TestResolveUnavailableWithoutDatabaseFile(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "missing.mmdb"))
	defer closeTestStore(t, store)

	if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestStoreReloadsReplacedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	resolved, err := store.Resolve(netip.MustParseAddr("8.8.8.8"))
	if err != nil || resolved.Point.City != "Shenzhen" {
		t.Fatalf("unexpected initial resolution %#v %v", resolved, err)
	}

	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Beijing", "Beijing", "BJ", "CN", "China",
			"Asia/Shanghai", 39.9, 116.4, 30),
	})
	store.reloadIfChanged()

	resolved, err = store.Resolve(netip.MustParseAddr("8.8.8.8"))
	if err != nil || resolved.Point.City != "Beijing" {
		t.Fatalf("unexpected reloaded resolution %#v %v", resolved, err)
	}
}

func TestResolveHandlesIPv4MappedIPv6(t *testing.T) {
	path := writeFixture(t, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	resolved, err := store.Resolve(netip.MustParseAddr("::ffff:8.8.8.8"))
	if err != nil || resolved.Point.City != "Shenzhen" {
		t.Fatalf("unexpected mapped resolution %#v %v", resolved, err)
	}
}

func TestStoreSurvivesConcurrentLookupsDuringReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	stop := make(chan struct{})
	errorsFound := make(chan error, 1)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != nil {
						select {
						case errorsFound <- err:
						default:
						}
						return
					}
				}
			}
		}()
	}
	for round := range 4 {
		writeMMDB(path, map[netip.Prefix][]byte{
			netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("City", "Region", "RG", "CN", "China",
				"Asia/Shanghai", 39.9, 116.4, 30),
		})
		store.reloadIfChanged()
		_ = round
	}
	close(stop)
	workers.Wait()
	select {
	case err := <-errorsFound:
		t.Fatalf("concurrent resolve failed during reload: %v", err)
	default:
	}
}

func TestStoreLifecycleIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	store := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store.Start()
	store.Start()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Start after Close must not resurrect the poller or the reader.
	store.Start()
	if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable after Start-after-Close, got %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLookupCompletesWhileReloadWaitsForFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	opened := make(chan struct{})
	release := make(chan struct{})
	var openedOnce sync.Once
	var releaseOnce sync.Once
	var calls atomic.Int32
	signalOpened := func() { openedOnce.Do(func() { close(opened) }) }
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	store := newTestStoreWithOpener(t, path, func(p string) (*geoip2.Reader, error) {
		if calls.Add(1) == 1 {
			return geoip2.Open(p)
		}
		signalOpened()
		select {
		case <-release:
		case <-time.After(testWaitTimeout):
		}
		return geoip2.Open(p)
	})
	defer closeTestStore(t, store)
	t.Cleanup(releaseAll)

	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Beijing", "Beijing", "BJ", "CN", "China",
			"Asia/Shanghai", 39.9, 116.4, 30),
	})
	reloadDone := make(chan struct{})
	go func() {
		store.reloadIfChanged()
		close(reloadDone)
	}()
	select {
	case <-opened:
	case <-time.After(testWaitTimeout):
		t.Fatal("reload did not reach the opener")
	}

	resolved, err := store.Resolve(netip.MustParseAddr("8.8.8.8"))
	if err != nil || resolved.Point.City != "Shenzhen" {
		t.Fatalf("lookup during pending reload failed: %#v %v", resolved, err)
	}
	releaseAll()
	select {
	case <-reloadDone:
	case <-time.After(testWaitTimeout):
		t.Fatal("reload did not complete")
	}

	resolved, err = store.Resolve(netip.MustParseAddr("8.8.8.8"))
	if err != nil || resolved.Point.City != "Beijing" {
		t.Fatalf("reloaded lookup failed: %#v %v", resolved, err)
	}
}

func TestReloadDuringCloseDoesNotResurrectReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	opened := make(chan struct{})
	release := make(chan struct{})
	var openedOnce sync.Once
	var releaseOnce sync.Once
	var calls atomic.Int32
	signalOpened := func() { openedOnce.Do(func() { close(opened) }) }
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	store := newStore(path, slog.New(slog.NewTextHandler(io.Discard, nil)), func(p string) (*geoip2.Reader, error) {
		if calls.Add(1) == 1 {
			return geoip2.Open(p)
		}
		signalOpened()
		select {
		case <-release:
		case <-time.After(testWaitTimeout):
		}
		return geoip2.Open(p)
	})
	store.Start()
	t.Cleanup(releaseAll)

	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Beijing", "Beijing", "BJ", "CN", "China",
			"Asia/Shanghai", 39.9, 116.4, 30),
	})
	reloadDone := make(chan struct{})
	go func() {
		store.reloadIfChanged()
		close(reloadDone)
	}()
	select {
	case <-opened:
	case <-time.After(testWaitTimeout):
		t.Fatal("reload did not reach the opener")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	releaseAll()
	select {
	case <-reloadDone:
	case <-time.After(testWaitTimeout):
		t.Fatal("reload did not complete")
	}

	if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable after close during reload, got %v", err)
	}
}

func TestCloseBeforeStartLeavesStoreInactive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	store := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store.Start()
	if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable after Close-before-Start, got %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenErrorClosesPartiallyOpenedReader(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("cannot count file descriptors on this platform")
	}
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDBWithType(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	}, "Bogus-Database-Type")
	store := newTestStore(t, path)
	defer closeTestStore(t, store)

	before := countOpenFileDescriptors(t)
	for range 50 {
		store.reloadIfChanged()
	}
	after := countOpenFileDescriptors(t)
	if after > before+2 {
		t.Fatalf("failed reloads leaked file descriptors: before=%d after=%d", before, after)
	}
	if _, err := store.Resolve(netip.MustParseAddr("8.8.8.8")); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable for a rejected database, got %v", err)
	}
}

func TestCloseReturnsWhilePollIsStuckInOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Shenzhen", "Guangdong", "GD", "CN", "China",
			"Asia/Shanghai", 22.5, 114.1, 50),
	})
	opened := make(chan struct{})
	release := make(chan struct{})
	var openedOnce sync.Once
	var calls atomic.Int32
	store := newStoreWithInterval(path, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(p string) (*geoip2.Reader, error) {
			if calls.Add(1) == 1 {
				return geoip2.Open(p)
			}
			openedOnce.Do(func() { close(opened) })
			select {
			case <-release:
			case <-time.After(testWaitTimeout):
			}
			return geoip2.Open(p)
		}, 10*time.Millisecond)
	store.Start()
	defer func() {
		_ = store.Close()
	}()

	writeMMDB(path, map[netip.Prefix][]byte{
		netip.MustParsePrefix("8.8.8.0/24"): mmdbCity("Beijing", "Beijing", "BJ", "CN", "China",
			"Asia/Shanghai", 39.9, 116.4, 30),
	})
	select {
	case <-opened:
	case <-time.After(testWaitTimeout):
		t.Fatal("poll did not reach the blocked opener")
	}
	closed := make(chan struct{})
	go func() {
		_ = store.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked behind a poll stuck in file I/O")
	}
	close(release)
	select {
	case <-store.stopped:
	case <-time.After(testWaitTimeout):
		t.Fatal("poll did not stop after the opener was released")
	}
}

func countOpenFileDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func newTestStore(t *testing.T, path string) *Store {
	t.Helper()
	return newTestStoreWithOpener(t, path, geoip2.Open)
}

func newTestStoreWithOpener(t *testing.T, path string,
	openReader func(string) (*geoip2.Reader, error)) *Store {
	t.Helper()
	store := newStore(path, slog.New(slog.NewTextHandler(io.Discard, nil)), openReader)
	store.Start()
	return store
}

func closeTestStore(t *testing.T, store *Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeFixture(t *testing.T, records map[netip.Prefix][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeMMDB(path, records)
	return path
}

// writeMMDB atomically replaces the database file like the external
// geoipupdate does, so readers that still map the previous inode stay valid.
func writeMMDB(path string, records map[netip.Prefix][]byte) {
	writeMMDBWithType(path, records, "GeoLite2-City")
}

func writeMMDBWithType(path string, records map[netip.Prefix][]byte, databaseType string) {
	temp := path + ".tmp"
	if err := os.WriteFile(temp, buildMMDBWithType(records, databaseType), 0o600); err != nil {
		panic(err)
	}
	if err := os.Rename(temp, path); err != nil {
		panic(err)
	}
}

// buildMMDB assembles a minimal valid MaxMind DB file with record size 24.
func buildMMDB(records map[netip.Prefix][]byte) []byte {
	return buildMMDBWithType(records, "GeoLite2-City")
}

func buildMMDBWithType(records map[netip.Prefix][]byte, databaseType string) []byte {
	entries := make([]struct {
		prefix netip.Prefix
		record []byte
	}, 0, len(records))
	for prefix, record := range records {
		entries = append(entries, struct {
			prefix netip.Prefix
			record []byte
		}{prefix, record})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].prefix.String() < entries[j].prefix.String()
	})

	var data []byte
	dataOffsets := make([]int, len(entries))
	for index, entry := range entries {
		dataOffsets[index] = len(data)
		data = append(data, entry.record...)
	}

	trie := &mmdbTrie{nodes: []mmdbNode{{left: -1, right: -1}}}
	for index, entry := range entries {
		trie.insert(entry.prefix, index)
	}
	nodeCount := len(trie.nodes)
	searchTree := trie.serialize(nodeCount, trie.leaves, dataOffsets)

	file := make([]byte, 0, len(searchTree)+16+len(data)+len(metadataMarker))
	file = append(file, searchTree...)
	file = append(file, make([]byte, 16)...)
	file = append(file, data...)
	file = append(file, metadataMarker...)
	metadata := mmdbMetadata(uint64(nodeCount), 1700000000, databaseType)
	file = append(file, metadata...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(metadata)))
	file = append(file, size[:]...)
	file = append(file, 0xEF, 0xCD, 0xAB)
	return file
}

var metadataMarker = []byte("\xAB\xCD\xEFMaxMind.com")

// mmdbTrie is a minimal binary search tree over IPv4 bit prefixes.
type mmdbTrie struct {
	nodes  []mmdbNode
	leaves []int // leaf markers resolve to record indices
}

// mmdbNode stores left/right records: -1 is null, non-negative values are
// node indices, negative values below -1 are leaf markers.
type mmdbNode struct {
	left, right int
}

func (t *mmdbTrie) insert(prefix netip.Prefix, recordIndex int) {
	address := prefix.Addr().As4()
	bits := prefix.Bits()
	node := 0
	for depth := 0; depth < bits-1; depth++ {
		bit := (address[depth/8] >> (7 - depth%8)) & 1
		child := t.child(node, bit)
		if child == -1 {
			child = len(t.nodes)
			t.nodes = append(t.nodes, mmdbNode{left: -1, right: -1})
			t.setChild(node, bit, child)
		}
		node = child
	}
	leaf := -(len(t.leaves) + 2)
	t.leaves = append(t.leaves, recordIndex)
	lastBit := (address[(bits-1)/8] >> (7 - (bits-1)%8)) & 1
	if lastBit == 0 {
		t.nodes[node].left = leaf
	} else {
		t.nodes[node].right = leaf
	}
}

func (t *mmdbTrie) child(node int, bit byte) int {
	if bit == 0 {
		return t.nodes[node].left
	}
	return t.nodes[node].right
}

func (t *mmdbTrie) setChild(node int, bit byte, child int) {
	if bit == 0 {
		t.nodes[node].left = child
	} else {
		t.nodes[node].right = child
	}
}

func (t *mmdbTrie) serialize(nodeCount int, leaves, dataOffsets []int) []byte {
	buffer := make([]byte, nodeCount*6)
	resolve := func(value int) uint32 {
		switch {
		case value == -1:
			return uint32(nodeCount)
		case value < -1:
			recordIndex := leaves[-(value + 2)]
			return uint32(nodeCount + 16 + dataOffsets[recordIndex])
		default:
			return uint32(value)
		}
	}
	for index, node := range t.nodes {
		left := resolve(node.left)
		right := resolve(node.right)
		buffer[index*6+0] = byte(left >> 16)
		buffer[index*6+1] = byte(left >> 8)
		buffer[index*6+2] = byte(left)
		buffer[index*6+3] = byte(right >> 16)
		buffer[index*6+4] = byte(right >> 8)
		buffer[index*6+5] = byte(right)
	}
	return buffer
}

func mmdbString(value string) []byte {
	out := []byte{byte(2<<5) | byte(len(value))}
	return append(out, value...)
}

func mmdbPair(key string, value []byte) []byte {
	out := mmdbString(key)
	return append(out, value...)
}

func mmdbMap(pairs ...[]byte) []byte {
	out := []byte{byte(7<<5) | byte(len(pairs))}
	for _, pair := range pairs {
		out = append(out, pair...)
	}
	return out
}

func mmdbSlice(items ...[]byte) []byte {
	out := []byte{byte(len(items)), 4}
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

func mmdbFloat64(value float64) []byte {
	out := []byte{byte(3<<5) | 8}
	bits := math.Float64bits(value)
	for shift := 56; shift >= 0; shift -= 8 {
		out = append(out, byte(bits>>uint(shift)))
	}
	return out
}

func mmdbUint(value uint64) []byte {
	switch {
	case value <= 0xFF:
		return []byte{0xA1, byte(value)}
	case value <= 0xFFFF:
		return []byte{0xA2, byte(value >> 8), byte(value)}
	case value <= 0xFFFFFFFF:
		return []byte{0xC4, byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}
	default:
		out := []byte{8, 2}
		for shift := 56; shift >= 0; shift -= 8 {
			out = append(out, byte(value>>uint(shift)))
		}
		return out
	}
}

func mmdbUint16(value uint16) []byte {
	return []byte{0xA2, byte(value >> 8), byte(value)}
}

func mmdbCity(cityName, regionName, regionCode, countryCode, countryName,
	timezone string, latitude, longitude float64, accuracy uint16) []byte {
	return mmdbMap(
		mmdbPair("city", mmdbMap(
			mmdbPair("names", mmdbMap(mmdbPair("en", mmdbString(cityName)))),
		)),
		mmdbPair("location", mmdbMap(
			mmdbPair("latitude", mmdbFloat64(latitude)),
			mmdbPair("longitude", mmdbFloat64(longitude)),
			mmdbPair("time_zone", mmdbString(timezone)),
			mmdbPair("accuracy_radius", mmdbUint16(accuracy)),
		)),
		mmdbPair("country", mmdbMap(
			mmdbPair("iso_code", mmdbString(countryCode)),
			mmdbPair("names", mmdbMap(mmdbPair("en", mmdbString(countryName)))),
		)),
		mmdbPair("subdivisions", mmdbSlice(
			mmdbMap(
				mmdbPair("iso_code", mmdbString(regionCode)),
				mmdbPair("names", mmdbMap(mmdbPair("en", mmdbString(regionName)))),
			),
		)),
	)
}

func mmdbCountryOnly(countryCode string) []byte {
	return mmdbMap(
		mmdbPair("country", mmdbMap(
			mmdbPair("iso_code", mmdbString(countryCode)),
			mmdbPair("names", mmdbMap(mmdbPair("en", mmdbString("Some Country")))),
		)),
	)
}

func mmdbMetadata(nodeCount, buildEpoch uint64, databaseType string) []byte {
	return mmdbMap(
		mmdbPair("binary_format_major_version", mmdbUint(2)),
		mmdbPair("binary_format_minor_version", mmdbUint(0)),
		mmdbPair("build_epoch", mmdbUint(buildEpoch)),
		mmdbPair("database_type", mmdbString(databaseType)),
		mmdbPair("description", mmdbMap(mmdbPair("en", mmdbString("Test database")))),
		mmdbPair("ip_version", mmdbUint(4)),
		mmdbPair("languages", mmdbSlice(mmdbString("en"))),
		mmdbPair("node_count", mmdbUint(nodeCount)),
		mmdbPair("record_size", mmdbUint(24)),
	)
}
