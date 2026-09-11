// Package bench holds published provider IP range corpora and the comparison
// that runs over them.
//
// It is a separate module from the library on purpose. The comparison depends
// on github.com/gaissmai/bart, which requires Go 1.24, and on two more
// libraries besides; none of that should reach a caller who only wants ipspan.
// Keeping the corpora here also keeps half a megabyte of data out of every
// `go get` of the library.
package bench

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Corpus is one provider's published ranges.
type Corpus struct {
	Name   string
	Source string
	V4, V6 []*net.IPNet
}

// Load reads every corpus in dir, which defaults to ./testdata.
func Load(dir string) ([]Corpus, error) {
	if dir == "" {
		dir = "testdata"
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.cidr"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	out := make([]Corpus, 0, len(files))
	for _, f := range files {
		c, err := loadFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no corpora in %s; run ./fetch.sh", dir)
	}
	return out, nil
}

func loadFile(path string) (Corpus, error) {
	fh, err := os.Open(path)
	if err != nil {
		return Corpus{}, err
	}
	defer fh.Close()

	c := Corpus{Name: strings.TrimSuffix(filepath.Base(path), ".cidr")}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if s, ok := strings.CutPrefix(line, "# source: "); ok {
				c.Source = s
			}
			continue
		}
		_, n, err := net.ParseCIDR(line)
		if err != nil {
			// A provider list that gains a malformed entry should be visible,
			// not silently shrink the corpus.
			return Corpus{}, fmt.Errorf("bad CIDR %q", line)
		}
		if n.IP.To4() != nil {
			c.V4 = append(c.V4, n)
		} else {
			c.V6 = append(c.V6, n)
		}
	}
	return c, sc.Err()
}
