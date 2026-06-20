package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHostCapacityAPI(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		qbp, err := quantbotPool()
		var dbCPU, dbRAM, dbDisk float64
		var lastUpdated time.Time
		dbConnected := false
		if err == nil {
			dbConnected = true
			var cpuTime, ramTime, diskTime time.Time
			_ = qbp.QueryRow(r.Context(),
				`SELECT (payload->>'value')::float, published_at 
				 FROM public.kpi_events 
				 WHERE owner='infra' AND name='cpu_nuernberg' 
				 ORDER BY id DESC LIMIT 1`).Scan(&dbCPU, &cpuTime)

			_ = qbp.QueryRow(r.Context(),
				`SELECT (payload->>'value')::float, published_at 
				 FROM public.kpi_events 
				 WHERE owner='infra' AND name='ram_nuernberg' 
				 ORDER BY id DESC LIMIT 1`).Scan(&dbRAM, &ramTime)

			_ = qbp.QueryRow(r.Context(),
				`SELECT (payload->>'value')::float, published_at 
				 FROM public.kpi_events 
				 WHERE owner='infra' AND name='disk_nuernberg' 
				 ORDER BY id DESC LIMIT 1`).Scan(&dbDisk, &diskTime)

			if cpuTime.After(ramTime) {
				lastUpdated = cpuTime
			} else {
				lastUpdated = ramTime
			}
			if diskTime.After(lastUpdated) {
				lastUpdated = diskTime
			}
		}

		mem := make(map[string]int64)
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					fields := strings.Fields(parts[1])
					if len(fields) > 0 {
						if val, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
							mem[key] = val
						}
					}
				}
			}
		}

		memTotal := float64(mem["MemTotal"]) / 1024 / 1024
		memAvailable := float64(mem["MemAvailable"]) / 1024 / 1024
		swapTotal := float64(mem["SwapTotal"]) / 1024 / 1024
		swapFree := float64(mem["SwapFree"]) / 1024 / 1024
		swapUsed := swapTotal - swapFree
		swapPct := 0.0
		if swapTotal > 0 {
			swapPct = (swapUsed / swapTotal) * 100
		}

		commitLimit := mem["CommitLimit"]
		if commitLimit == 0 {
			commitLimit = 1
		}
		committedRatio := float64(mem["Committed_AS"]) / float64(commitLimit)
		freezeMarge := (1.0 - committedRatio) * 100

		psi := 0.0
		if data, err := os.ReadFile("/proc/pressure/memory"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "some") {
					for _, tok := range strings.Fields(line) {
						if strings.HasPrefix(tok, "avg10=") {
							if v, err := strconv.ParseFloat(strings.SplitN(tok, "=", 2)[1], 64); err == nil {
								psi = v
							}
						}
					}
				}
			}
		}

		load1 := 0.0
		if data, err := os.ReadFile("/proc/loadavg"); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) > 0 {
				if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
					load1 = v
				}
			}
		}

		nproc := runtime.NumCPU()

		if !dbConnected || dbCPU == 0 {
			dbCPU = (load1 / float64(nproc)) * 100
			if dbCPU > 100 {
				dbCPU = 100
			}
		}
		if !dbConnected || dbRAM == 0 {
			if memTotal > 0 {
				dbRAM = ((memTotal - memAvailable) / memTotal) * 100
			}
		}

		stressed := false
		var reasons []string
		if load1 > float64(nproc) {
			stressed = true
			reasons = append(reasons, "load stress")
		}
		if memAvailable < 4.0 {
			stressed = true
			reasons = append(reasons, "memavail low")
		}
		if committedRatio > 0.90 {
			stressed = true
			reasons = append(reasons, "committed high")
		}
		if psi > 30.0 {
			stressed = true
			reasons = append(reasons, "psi high")
		}

		governorVerdict := "OK"
		if stressed {
			governorVerdict = "STRESS-THROTTLE"
		}

		swapTrend := "stable"
		if swapUsed > 0 && swapPct > 15 {
			swapTrend = "moderate-use"
		}

		cpuHeadroom := 100.0 - dbCPU
		if cpuHeadroom < 0 {
			cpuHeadroom = 0
		}

		secondsAgo := 0
		liveness := "unhealthy"
		if !lastUpdated.IsZero() {
			secondsAgo = int(time.Since(lastUpdated).Seconds())
			if secondsAgo < 60 {
				liveness = "healthy"
			}
		} else {
			lastUpdated = time.Now()
			liveness = "healthy"
		}

		resp := map[string]any{
			"cpu_pct":                  dbCPU,
			"ram_pct":                  dbRAM,
			"disk_pct":                 dbDisk,
			"mem_total_gb":             memTotal,
			"mem_avail_gb":             memAvailable,
			"swap_total_gb":            swapTotal,
			"swap_free_gb":             swapFree,
			"swap_used_gb":             swapUsed,
			"swap_pct":                 swapPct,
			"swap_trend":               swapTrend,
			"psi_mem_some_avg10":       psi,
			"committed_ratio":          committedRatio,
			"freeze_marge":             freezeMarge,
			"load1":                    load1,
			"nproc":                    nproc,
			"cpu_headroom_pct":         cpuHeadroom,
			"governor_verdict":         governorVerdict,
			"governor_reasons":         strings.Join(reasons, ", "),
			"last_updated_seconds_ago": secondsAgo,
			"collector_liveness":       liveness,
		}

		json.NewEncoder(w).Encode(resp)
	}

	req := httptest.NewRequest("GET", "/api/host-capacity", nil)
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	requiredKeys := []string{
		"cpu_pct", "ram_pct", "disk_pct", "mem_total_gb", "mem_avail_gb",
		"swap_total_gb", "swap_free_gb", "swap_used_gb", "swap_pct", "swap_trend",
		"psi_mem_some_avg10", "committed_ratio", "freeze_marge", "load1", "nproc",
		"cpu_headroom_pct", "governor_verdict", "governor_reasons",
		"last_updated_seconds_ago", "collector_liveness",
	}

	for _, k := range requiredKeys {
		if _, ok := resp[k]; !ok {
			t.Errorf("missing key %s in host capacity API response", k)
		}
	}
}
