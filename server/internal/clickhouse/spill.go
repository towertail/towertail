package clickhouse

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const spillExt = ".ndjson.gz"

// spill keeps the batches that ClickHouse did not take, as gzip NDJSON
// files in one folder. The total size stays below max.
type spill struct {
	dir string
	max int64
	mu  sync.Mutex
}

func newSpill(dir string, max int64) (*spill, error) {
	if dir == "" {
		return nil, errors.New("clickhouse: spill dir not set")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spill dir: %w", err)
	}
	s := &spill{dir: dir, max: max}
	s.updateMetric()
	return s, nil
}

// write saves the bundles to a new file. It writes a tmp file first and
// then renames it, so a reader never sees a part of a file.
func (s *spill) write(bundles []IngestBundle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.updateMetric()

	tmp, err := os.CreateTemp(s.dir, "spill-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	gz := gzip.NewWriter(tmp)
	enc := json.NewEncoder(gz)
	for _, b := range bundles {
		if err := enc.Encode(b); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := gz.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	st, err := os.Stat(tmp.Name())
	if err != nil {
		return err
	}
	if s.size()+st.Size() > s.max {
		return fmt.Errorf("clickhouse: spill full (max %d bytes)", s.max)
	}
	name := fmt.Sprintf("%020d%s", time.Now().UnixNano(), spillExt)
	return os.Rename(tmp.Name(), filepath.Join(s.dir, name))
}

// oldest gives the path of the oldest spill file.
func (s *spill) oldest() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := s.files()
	if len(files) == 0 {
		return "", false
	}
	return files[0], true
}

func (s *spill) read(path string) ([]IngestBundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	var out []IngestBundle
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 128*1024), 16*1024*1024)
	for sc.Scan() {
		var b IngestBundle
		if err := json.Unmarshal(sc.Bytes(), &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, sc.Err()
}

func (s *spill) remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(path)
	s.updateMetric()
}

// reject moves a file that cannot be read to <name>.bad, so the replay
// does not try it again.
func (s *spill) reject(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Rename(path, path+".bad")
	s.updateMetric()
}

// files gives the spill files, oldest first. The caller holds mu.
func (s *spill) files() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), spillExt) {
			out = append(out, filepath.Join(s.dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// size is the total size of the spill files. The caller holds mu.
func (s *spill) size() int64 {
	var n int64
	for _, p := range s.files() {
		if st, err := os.Stat(p); err == nil {
			n += st.Size()
		}
	}
	return n
}

func (s *spill) updateMetric() {
	batcherSpillBytes.Set(float64(s.size()))
}
