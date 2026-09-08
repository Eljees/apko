// Copyright 2026 Chainguard, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package build

import (
	"archive/tar"
	"math/rand"
	"os"
	"testing"
)

// buildTestLayer writes a deterministic mixed-compressibility layer (large
// enough to span several pgzip blocks) in the requested mode, returning the
// layer and the directory holding its files.
func buildTestLayer(t *testing.T, compressed bool) (*layer, string) {
	t.Helper()

	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "layer-*.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	lw := newLayerWriter(f)
	if compressed {
		if lw, err = newCompressedLayerWriter(f, 4); err != nil {
			t.Fatal(err)
		}
	}

	content := make([]byte, 3<<20)
	rand.New(rand.NewSource(0x2781)).Read(content[:1<<20]) // 1 MiB incompressible, 2 MiB zeros
	hdr := &tar.Header{Name: "usr/blob", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}
	if err := lw.w.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := lw.w.Write(content); err != nil {
		t.Fatal(err)
	}

	l, err := lw.finalize()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	return l, dir
}

func TestCompressedLayerEquivalence(t *testing.T) {
	legacy, legacyDir := buildTestLayer(t, false)
	single, singleDir := buildTestLayer(t, true)

	legacyDiff, err := legacy.DiffID()
	if err != nil {
		t.Fatal(err)
	}
	// The legacy Digest call below populates the process-global
	// compressionCache; without this cleanup, a repeat run (-count>1) takes
	// the cache-hit path and never creates the legacy .gz file.
	t.Cleanup(func() { compressionCache.Delete(legacyDiff.String()) })
	singleDiff, err := single.DiffID()
	if err != nil {
		t.Fatal(err)
	}
	if legacyDiff != singleDiff {
		t.Errorf("DiffID: got = %v, want = %v", singleDiff, legacyDiff)
	}

	legacyDigest, err := legacy.Digest() // triggers the legacy second-pass compression
	if err != nil {
		t.Fatal(err)
	}
	singleDigest, err := single.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if legacyDigest != singleDigest {
		t.Errorf("compressed digest: got = %v, want = %v", singleDigest, legacyDigest)
	}

	legacySize, err := legacy.Size()
	if err != nil {
		t.Fatal(err)
	}
	singleSize, err := single.Size()
	if err != nil {
		t.Fatal(err)
	}
	if legacySize != singleSize {
		t.Errorf("compressed size: got = %d, want = %d", singleSize, legacySize)
	}

	if got := countFiles(t, singleDir); got != 1 {
		t.Errorf("single-pass files: got = %d, want = 1", got)
	}
	if got := countFiles(t, legacyDir); got != 2 {
		t.Errorf("legacy files: got = %d, want = 2 (plain + gz)", got)
	}

	raw, err := os.ReadFile(single.compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Errorf("compressed file does not start with gzip magic: % x", raw[:min(2, len(raw))])
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestCompressedLayerWriterAbort(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "layer-*.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	lw, err := newCompressedLayerWriter(f, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := lw.w.WriteHeader(&tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}

	lw.Abort()
	lw.Abort() // idempotent

	if _, err := lw.finalize(); err == nil {
		t.Error("finalize after Abort: got = nil, want error")
	}
}
