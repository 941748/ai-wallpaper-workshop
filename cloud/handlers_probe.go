package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"wallpaper/internal/probe"
)

// ---------- 公共探针池 ----------

var probeIDRe = regexp.MustCompile(`^p\d{2}$`)

func (a *API) handleProbeSample(w http.ResponseWriter, r *http.Request) {
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count <= 0 || count > probe.FirstCount {
		count = probe.FirstCount
	}
	combos := probe.BalancedCombos(probe.FirstCount)
	items := make([]map[string]any, 0, count)
	for i, cb := range combos {
		if i >= count {
			break
		}
		items = append(items, map[string]any{
			"id":     cb.ID,
			"values": cb.Values,
			"seed":   cb.Seed,
			"url":    "/api/v1/probes/image?id=" + cb.ID,
		})
	}
	writeOK(w, map[string]any{"items": items})
}

func (a *API) handleProbeImage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !probeIDRe.MatchString(id) {
		writeFail(w, http.StatusBadRequest, "非法探针 ID")
		return
	}
	path := filepath.Join(a.dataDir, "probes", id+".png")
	if _, err := os.Stat(path); err != nil {
		writeFail(w, http.StatusNotFound, "探针图尚未就绪")
		return
	}
	serveFile(w, path)
}
