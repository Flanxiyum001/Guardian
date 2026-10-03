package main

import (
	"bytes"
	"io"
	"regexp"
	"strings"
)

// tokenRe matches the placeholders produced by maskText.
var tokenRe = regexp.MustCompile(`\[MASKED_[A-Za-z0-9_]+_[0-9]+\]`)

const tokenMarker = "[MASKED_"

// isPartialToken reports whether tail could be the beginning of a token that is
// still arriving, e.g. "[MAS" or "[MASKED_EMAIL_ADDRESS_17". Such a tail must be
// held back until the next chunk, otherwise rehydration would corrupt a token
// split across two SSE chunks.
func isPartialToken(tail string) bool {
	if tail == "" {
		return false
	}
	if len(tail) <= len(tokenMarker) {
		return strings.HasPrefix(tokenMarker, tail)
	}
	if !strings.HasPrefix(tail, tokenMarker) || strings.ContainsRune(tail, ']') {
		return false
	}
	for _, c := range tail {
		switch {
		case c >= '0' && c <= '9', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_', c == '[':
		default:
			return false
		}
	}
	return true
}

// resolveTokens swaps every complete token in data for its original value,
// memoising lookups so repeated tokens cost one Redis round trip.
func resolveTokens(data []byte, res tokenResolver, cache map[string]string) []byte {
	return tokenRe.ReplaceAllFunc(data, func(m []byte) []byte {
		tok := string(m)
		if v, ok := cache[tok]; ok {
			return []byte(v)
		}
		if v, ok := res.Resolve(tok); ok {
			cache[tok] = v
			return []byte(v)
		}
		return m // unknown or expired token: leave it visible
	})
}

// rehydrateAll rehydrates a fully-buffered body.
func rehydrateAll(raw []byte, res tokenResolver) []byte {
	return resolveTokens(raw, res, map[string]string{})
}

// rehydrateReader streams an upstream body to the client while replacing tokens
// with their originals, holding back partial tokens across chunk boundaries.
type rehydrateReader struct {
	src      io.ReadCloser
	res      tokenResolver
	cache    map[string]string
	buf      []byte
	ready    []byte
	eof      bool
	emitted  int
	closed   bool
	finalize func(outputBytes int)
}

func newRehydrateReader(src io.ReadCloser, res tokenResolver, finalize func(int)) *rehydrateReader {
	return &rehydrateReader{
		src:      src,
		res:      res,
		cache:    map[string]string{},
		finalize: finalize,
	}
}

func (r *rehydrateReader) Read(p []byte) (int, error) {
	for len(r.ready) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		chunk := make([]byte, 16*1024)
		n, err := r.src.Read(chunk)
		if n > 0 {
			r.buf = append(r.buf, chunk[:n]...)
			r.process(false)
		}
		switch {
		case err == io.EOF:
			r.eof = true
			r.process(true)
		case err != nil:
			return 0, err
		}
	}

	n := copy(p, r.ready)
	r.emitted += n
	r.ready = r.ready[n:]
	return n, nil
}

// process rehydrates the buffer, keeping any trailing partial token for later.
func (r *rehydrateReader) process(final bool) {
	if len(r.buf) == 0 {
		return
	}
	resolved := resolveTokens(r.buf, r.res, r.cache)

	if !final {
		if i := bytes.LastIndexByte(resolved, '['); i >= 0 && isPartialToken(string(resolved[i:])) {
			r.ready = append(r.ready, resolved[:i]...)
			r.buf = append(r.buf[:0], resolved[i:]...)
			return
		}
	}

	r.ready = append(r.ready, resolved...)
	r.buf = r.buf[:0]
}

func (r *rehydrateReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.src.Close()
	if r.finalize != nil {
		r.finalize(r.emitted)
	}
	return err
}
