# ArchSim — Plan Produk & Teknis

> *Working name:* **ArchSim** (bisa diganti).
> Simulator arsitektur sistem berbasis web: drag & drop komponen production-real, set protokol komunikasi antar service, set config sesuai kapabilitas aslinya (termasuk autoscaling K8s), lalu uji "arsitektur ini kuat di berapa TPS?".
>
> Disusun oleh: Samsul · Tanggal: Oktober 2026 · Status: Draft v1.1

---

## 0. Revisi v1.1 (keputusan setelah review)

Bagian ini **mengalahkan** isi bagian lain jika bertentangan.

**Model simulasi**
1. **Time-stepped = hybrid (pointwise stationary approximation):** antrian fluid hanya untuk backlog saat λ > μ; saat ρ < 1, waktu tunggu tiap tick memakai M/M/c (Erlang C + Kingman). Dengan begitu mode analitis & time-stepped sepakat di steady-state (syarat validasi 9.2).
2. **Request class / flow di level workload** (mis. `read` 80%, `write` 20%), bukan split per edge. Edge punya filter `classes`; biaya CPU service bisa berbeda per class. Kondisi cache diekspresikan sebagai routing probabilistik (`probability` atau `when: "cache_miss:<cacheId>"`).
3. **Fan-out eksplisit:** edge dengan `parallelGroup` yang sama dari caller yang sama berjalan paralel (latency = max); selain itu berurutan (latency = jumlah).
4. **Degradasi kontensi = Universal Scalability Law** (`σ`, `κ`) per replica — parameter bisa di-fit dari load test user (fondasi fitur kalibrasi).
5. **Max sustainable TPS** memakai visit ratio + pencarian (exponential + bisection) atas evaluasi penuh, sehingga constraint non-linear (pool koneksi, SLO p99, HPA, scheduling) ikut terhitung.
6. **K8s `requests` vs `limits` dibedakan:** HPA relatif ke `requests.cpu`; kapasitas/throttling ke `limits.cpu`; penjadwalan pod memakai *allocatable* node (formula reservasi GKE) dengan bin-packing first-fit-decreasing.
7. **Retry** di time-stepped masuk ke tick berikutnya (bukan iterasi fixed-point). Mode analitis **tidak** memodelkan retry (dilaporkan sebagai asumsi).
8. **Datastore:** biaya CPU per operasi (`read`/`write`/`complex`), commit latency, primary vs read replica, dan **koneksi aktif yang dibutuhkan (λ × R) vs `max_connections`** + peringatan jika total pool semua replica > `max_connections`.

**Teknis & scope**
9. **Fase 0–2 local-first:** desain disimpan di browser + export/import file + share link (JSON terkompresi di URL hash). Backend Go hanya menyajikan web app di belakang **auth single-user sederhana** (kredensial dari env, password bcrypt, cookie sesi bertanda tangan HMAC, rate-limit login) supaya instance publik tidak bisa dipakai orang lain. DB/OAuth/billing ditunda ke Fase 3.
10. **Satu sumber kebenaran katalog:** YAML di `engine/catalog/data/` (di-embed ke Go via `go:embed`, jadi ikut ke WASM & CLI). JSON untuk web digenerate dari katalog yang sama. Tidak ada bahasa ekspresi di default (nilai turunan dihitung di kode archetype).
11. **WASM jalan di Web Worker**, di-lazy-load, quick check di-debounce.

**Produk**
12. **Time-stepped versi minimal (HPA + spike) masuk MVP** — diferensiator utama harus terlihat oleh beta tester. Free tier dibatasi durasi/jumlah run, bukan fiturnya.
13. Memory sebagai constraint scheduling (requests memory) ikut dihitung; OOM/eviction dinamis di Fase 2.

**Archetype final** (perluasan tabel 5.1): `client`, `edge_proxy`, `gateway`, `compute_service`, `runtime_host` (VM group/ASG), `orchestrator` (K8s Deployment + HPA), `node_pool` (K8s node pool + cluster autoscaler), `datastore`, `cache`, `queue_stream`, `external`.

**Catatan implementasi Fase 1 (Oktober 2026)**
- Kedua mode tinggal di satu paket `engine/sim` karena berbagi graph hasil kompilasi: `Run` (quick check) dan `Simulate` (time simulation). Tidak ada paket `timestep/` terpisah.
- **Model time simulation:** tick 1 detik. Beban mengalir lewat call tree dalam urutan topologis. Tiap station melayani maksimal kapasitasnya per tick; kelebihannya menjadi backlog fluid (FIFO, dibagi proporsional terhadap work). Backlog yang menunggu lebih lama dari `queueTimeoutMs` (default 30 s) di-drop dan dihitung sebagai error. Latency per tick memakai M/M/c pointwise-stationary pada laju yang benar-benar dilayani, ditambah wait FIFO di station yang jenuh, lalu disampel Monte Carlo sehingga `timeoutMs` di edge ikut menghasilkan error. Di steady state hasilnya cocok dengan quick check (golden test).
- **Kontrol autoscaling:** HPA membaca CPU yang *terlayani* (rata-rata per sync period). Pod yang jenuh tidak bisa melaporkan demand di atas limit/request-nya, dan pod yang belum Ready tidak punya metrik, sehingga scale-up saat overload bertahap, seperti di Kubernetes. Policy-nya meliputi tolerance 0.1, scale-up max(100%, 4 pod)/15 s, dan stabilization scale-down 300 s. Durasi pod startup, provisioning node oleh cluster autoscaler, serta boot/evaluasi/cooldown VM ASG semuanya bersumber di katalog.
- **Belum dimodelkan di time simulation** (Fase 2): retry, circuit breaker, failure injection, scale-down cluster autoscaler, dan memory pressure. Wait FIFO fluid bersifat deterministik, jadi ketika wait melewati timeout edge, *semua* request di tick itu timeout (efek "cliff"), bukan sebagian seperti pada distribusi wait sungguhan.
- **Web:** React 18 + `@xyflow/react` 12 + Zustand + Tailwind 4 + ECharts 6 (tooltip richText, tanpa HTML). Form config digenerate dari param katalog (`options` / `optionsFrom` di YAML). Desain disimpan dengan field `ui` (posisi canvas) yang diabaikan engine.

**Catatan implementasi Fase 2 (Oktober 2026)**
- **Retry** (`edge.retry`: `max`, `backoffMs`, `budgetPercent`): attempt yang gagal (error atau timeout) kembali sebagai retry di tick berikutnya. Jeda retry = latency kegagalan (diukur Monte Carlo) + backoff·2^(k-1), dibagi ke dua tick yang diapitnya, dan dilacak per generasi sampai `max`. `timeoutMs` berlaku per attempt. Budget membatasi retry sebagai persentase dari first attempt per tick (seperti RetryBudget Finagle / retry throttling gRPC); tanpa budget, beban callee bisa naik sampai (1+max)×. Contoh `engine/examples/retry-storm.json`: slowdown 30 detik membuat inventory menerima 4× beban dan gagal permanen (metastable); dengan budget 10% pulih begitu slowdown berakhir.
- **Circuit breaker** (`edge.circuitBreaker`): state machine closed → open → half-open per edge. Failure rate dihitung pada sliding window berbasis waktu (default 10 s, karena window default Resilience4j berbasis jumlah call). Default lainnya dari Resilience4j: threshold 50%, minimum 100 call, open 60 s, 10 probe/s. Selama open, call gagal cepat dan tidak di-retry. Saat half-open, semua attempt yang disampel mengukur kesehatan callee, tapi hanya sebagian yang benar-benar dikirim.
- **Failure injection** (`workload.timeline.failures`): `instance-loss` (node / VM / pod; controller mengganti yang dikelolanya setelah delay provisioning/startup, instance unmanaged kembali setelah `durationS`), `zone-outage` (setiap pool, VM group, dan service kehilangan ceil(n/zones); datastore, cache, dan LB diasumsikan multi-AZ), `outage` mode `error` atau `hang` (hang = callee tidak menjawab sampai timeout caller / `queueTimeoutMs`), `latency` (node atau edge), dan `failover` (primary datastore mati selama `failoverS` dari katalog, default 120 s menurut dokumentasi RDS Multi-AZ; read replica tetap melayani).
- **Pola beban:** `ramp`, `step`, `diurnal` (cosine, dengan `periodS` yang biasanya dikompresi sementara delay kontrol tetap real-time), di samping `constant` dan `spike`.
- **Scale-down cluster autoscaler:** setiap node yang tidak dibutuhkan dihapus setelah `scaleDownUnneededS` (600 s) dan tidak dalam `scaleDownDelayAfterAddS` (600 s) sejak scale-up terakhir (default flag CA). Pod dipadatkan dengan FFD, jadi node yang dihapus kosong. Pod tidak di-evict dan threshold utilisasi 50% tidak diterapkan.
- **Efek "cliff" dikurangi:** wait FIFO kini disampel merata antara backlog di awal tick dan di akhir tick, sehingga timeout terjadi bertahap saat antrian tumbuh melewati batasnya.
- **Compare:** "Pin baseline" menyimpan salinan desain (localStorage). Quick check dan time simulation dijalankan ulang pada salinan itu dan ditampilkan berdampingan dengan desain saat ini (tabel selisih, garis putus-putus abu-abu di grafik).
- **Belum:** memory pressure (OOM, eviction); thread/koneksi caller yang tertahan menunggu dependency yang hang; quick check tetap tidak memodelkan retry. **Lab kalibrasi v1 (kind + k6) sudah disiapkan di `lab/` tapi belum dijalankan** (direncanakan di VPS), jadi exit criterion Fase 2 (HPA cocok dengan eksperimen nyata) belum terbukti.

---

## 1. Ringkasan Eksekutif

- **Masalah:** diagram arsitektur (draw.io, Excalidraw, Eraser) itu statis — tidak bisa menjawab "kuat di berapa TPS, bottleneck di mana, apa yang terjadi saat spike atau node mati". Load test sungguhan butuh infra dan waktu.
- **Solusi:** canvas drag & drop + engine simulasi yang memodelkan kapasitas, antrian, latency, autoscaling, dan failure. Semua angka default bisa di-override user, dan setiap hasil diberi label tingkat keyakinan (estimasi kasar vs terkalibrasi).
- **Target user:** (1) engineer yang belajar / latihan system design, (2) engineer & tim yang melakukan capacity planning sungguhan.
- **Scope:** semua fitur simulasi (TPS, latency, bottleneck, autoscale, failure/chaos) **kecuali estimasi biaya cloud**.
- **Stack:** React (TypeScript) + Go.
- **Arah bisnis:** SaaS publik dengan model freemium.

**Kesimpulan riset singkat:** pasar *system design simulator* sudah ramai, tapi hampir semuanya fokus ke **interview prep** dengan komponen generik (LB, App Server, DB). Celah yang terbuka: **simulasi production-real untuk capacity planning** — konfigurasi produk asli (K8s HPA, Kong, instance type), autoscaling yang benar-benar disimulasikan, protokol di edge yang punya biaya nyata, dan **kalibrasi dari data load test / metrics user**. Detail di bagian 2.

---

## 2. Hasil Riset Pasar

### 2.1 Pemain yang sudah ada

| Produk | Fokus | Fitur menonjol | Harga |
|---|---|---|---|
| SysSimulator | Belajar + interview | Engine DES Rust→WASM di browser, 18 tipe komponen, 28 skenario chaos, 56 blueprint, estimasi biaya AWS | Gratis, tanpa signup |
| paperdraw.dev | Belajar | Chaos real-time (matikan replica, spike, latency jaringan), strategi LB, TTL cache, replica & connection pool DB | Gratis |
| SysLab | Interview prep | Skenario, AI mentor, mock interview | Free terbatas; Premium $99/tahun |
| Sketchy Systems | Belajar + interview | Model kecil tapi "jujur": retry, timeout, circuit breaker, retry storm, 28 challenge | US$14.99/bulan |
| System Design Sim | Interview (SDE-2 s/d staff) | Ukuran & harga AWS asli, 28 challenge | Gratis; Pro direncanakan |
| SystemForge (OSS) | Interview | 35 komponen, scoring ala interviewer | Open source |
| LOADLINE (OSS) | Capacity planning | Engine DES Go + ConnectRPC, katalog AWS/GCP/Cloudflare dipetakan ke 9 tipe generik, deterministik | Open source (MIT) |
| SyDe | Design + simulasi | Optimasi berbasis AI, klaim importer Terraform/K8s/Compose | — |
| ArchScope | Capacity planning ringan | Next.js + React Flow, algoritma rate limit, time-series | — |

Rincian sumber per produk:

- SysSimulator menjalankan discrete-event simulation (DES) Rust→WASM di browser, dengan 18 tipe komponen, 28 skenario chaos, 56 blueprint, dan estimasi biaya AWS tanpa signup.[1] Share link-nya disimpan di server dengan masa berlaku 7 hari.[1]
- paperdraw.dev gratis dan menonjolkan chaos engineering saat simulasi berjalan: mematikan replica DB, spike trafik, dan latency jaringan.[3] Komponennya mencakup LB (round robin / least connections), cache dengan TTL, DB dengan replica dan connection pool, serta queue.[3]
- SysLab menawarkan paket gratis (5 workspace, 120 menit simulasi/hari, 1 mock interview/hari) dan Premium $99/tahun dengan AI mentor.[4]
- Sketchy Systems berbayar US$14.99/bulan dengan trial 7 hari, punya 28 challenge, dan memodelkan retry, timeout, circuit breaker, termasuk retry storm.[5]
- System Design Sim memposisikan diri sebagai simulator gratis dengan ukuran dan harga AWS asli; paket Pro-nya masih direncanakan dan belum aktif.[9]
- SystemForge adalah simulator interview open source dengan 35 komponen dan penilaian per dimensi interview.[8]
- LOADLINE (open source, MIT) memakai engine DES di Go plus ConnectRPC, dengan katalog AWS/GCP/Cloudflare yang dipetakan ke 9 tipe komponen generik.[6]
- SyDe mengklaim fitur optimasi berbasis AI dan importer berbagai format arsitektur.[10]
- ArchScope dibangun dengan Next.js dan React Flow, dengan analisis bottleneck dan algoritma rate limiting.[11]

Di luar kategori simulator, tool diagram cloud (Cloudcraft, Eraser, dll.) membaca akun cloud lewat kredensial read-only lalu menggambar diagram — tetap statis, tanpa simulasi beban.[16]

### 2.2 Keterbatasan yang terlihat di kompetitor

- **Autoscaling jarang benar-benar disimulasikan.** LOADLINE, yang paling dekat ke capacity planning, secara eksplisit menulis bahwa autoscaling hanya "dideskripsikan, tidak disimulasikan" — jumlah instance statis per run.[7]
- **Protokol tidak berbiaya.** Di LOADLINE ukuran payload tidak menambah waktu transfer, hanya dipakai untuk menghitung biaya egress.[7] Retry-nya juga tanpa circuit breaker atau retry budget.[7]
- **Service time konstan.** Degradasi akibat contention, GC, atau lock tidak dimodelkan, sehingga ceiling-nya optimistis di dekat titik jenuh.[7]
- **Komponen generik.** Mayoritas memakai kotak abstrak ("App Server", "Database"), bukan produk asli dengan config aslinya — cocok untuk interview, kurang untuk keputusan production.
- **Kebutuhan nyata ada.** Seorang engineer yang sedang modernisasi arsitektur hybrid-cloud mengeluh bahwa simulator yang ia temukan "overly academic" dan sulit dipetakan ke arsitektur nyata.[12] Komentator di thread yang sama memodelkan sistem dengan DES (simpy): akurasinya sekitar ±8% pada sistem mainan, tapi kurang akurat untuk sistem production, dan ia menyarankan validasi terhadap metrics observability.[12]

### 2.3 Implikasi untuk positioning

1. **Jangan bersaing frontal di interview prep** — sudah padat dan banyak yang gratis. Interview/learning cukup jadi jalur akuisisi (template, mode belajar).
2. **Diferensiasi utama = "production-real capacity planning":**
   - Katalog komponen **produk asli** dengan config asli (K8s Deployment + HPA, Kong + plugin, EC2/GCE instance type, Postgres, Redis, Kafka, dll.).
   - **Autoscaling disimulasikan** sesuai algoritma aslinya (HPA, cluster autoscaler, ASG), termasuk delay dan stabilization window.
   - **Edge punya biaya nyata**: protokol, serialisasi, payload, network hop, connection pool, timeout/retry/circuit breaker.
   - **Kalibrasi**: user bisa memasukkan hasil load test / metrics sendiri → confidence naik. Ini menjawab langsung keluhan "sulit dipetakan ke arsitektur nyata".[12]
   - **Transparansi asumsi** di setiap angka (LOADLINE juga melakukan ini; ini sudah jadi "table stakes" untuk tool yang serius).[6]
3. **Monetisasi** lebih kuat di segmen tim/profesional (capacity planning, review arsitektur) dibanding individu pelajar yang sudah punya banyak opsi gratis.

---

## 3. Goals & Non-Goals

### Goals
- Canvas drag & drop dengan komponen infra, gateway, data store, messaging, dan runtime service.
- Edge = komunikasi, dengan protokol (REST/HTTP1.1, HTTP/2, gRPC, WebSocket, async via queue/stream) dan kebijakan (timeout, retry, circuit breaker).
- Config per komponen mengikuti kapabilitas aslinya, dengan **default ideal yang bisa di-override**.
- Simulasi: target TPS → verdict aman/waspada/overload, bottleneck, p50/p95/p99, error rate, max sustainable TPS, headroom per komponen.
- Autoscaling (K8s HPA, cluster autoscaler, VM autoscaling group) yang disimulasikan dalam dimensi waktu.
- Skenario: pola beban (konstan, ramp, spike, step, diurnal) dan failure injection.
- Label akurasi yang jujur: setiap hasil menyebut asumsi, sumber angka, dan tingkat confidence.

### Non-Goals (untuk sekarang)
- **Estimasi biaya cloud** (sesuai keputusan; bisa dipertimbangkan setelah product-market fit).
- Menjadi pengganti load test sungguhan — posisinya "sebelum load test" dan "pelengkap load test".
- Provisioning/deploy infra sungguhan (bukan Terraform generator).

---

## 4. Konsep Produk & Alur User

1. **Mulai:** blank canvas atau template (contoh: "REST API + Postgres", "Microservices di K8s + Kong", "Event-driven dengan Kafka").
2. **Desain:** drag komponen dari palette → taruh di canvas → komponen bisa bersarang (Service di dalam K8s Deployment di dalam Node Pool di dalam Region/AZ).
3. **Hubungkan:** tarik garis → pilih protokol & kebijakan edge → set "calls per request" (fan-out) dan rasio (misal 80% read, 20% write).
4. **Konfigurasi:** klik komponen → panel config (form otomatis dari skema katalog). Setiap field menunjukkan: nilai default, sumber default, dan tombol "pakai angka saya".
5. **Workload:** set entry point + target TPS (atau turunan dari DAU × request/user × peak multiplier), pola beban, durasi, dan SLO (misal p99 < 300 ms, error < 0.1%).
6. **Run:**
   - **Quick check (analitis):** instan, hasil steady-state.
   - **Simulasi waktu:** timeline per detik — autoscaling, antrian, spike, failure.
7. **Hasil:** verdict, bottleneck (berurutan), heatmap utilisasi di canvas, grafik timeline, panel asumsi & confidence, rekomendasi ("tambah replica X", "naikkan maxReplicas HPA", "tambah cache di depan DB").
8. **Iterasi & bandingkan:** duplikat desain → bandingkan dua versi di workload yang sama.
9. **Simpan/share/export:** link share, export PNG/PDF report (fitur berbayar untuk report).

---

## 5. Model Domain

### 5.1 Prinsip: Archetype + Catalog (komponen = data, bukan kode)

Engine hanya mengenal sedikit **archetype** perilaku. Produk spesifik adalah **entri katalog** (YAML) yang memetakan config asli ke parameter archetype. Menambah komponen = menambah file katalog, tanpa mengubah engine.

| Archetype | Perilaku inti | Contoh entri katalog |
|---|---|---|
| `client` | Sumber trafik / workload | Web users, Mobile, Partner API |
| `edge_proxy` | Proxy L4/L7, per-request CPU cost, conn limit | AWS ALB/NLB, Nginx, Envoy, Cloudflare |
| `gateway` | Proxy + plugin chain (auth, rate limit) | Kong, AWS API Gateway, Apigee |
| `compute_service` | Worker pool, CPU time + IO wait per request, concurrency | Service Go / Rust / Node / Java / Python |
| `runtime_host` | Penyedia kapasitas (vCPU, mem, network) | EC2, GCE, VM on-prem, K8s Node |
| `orchestrator` | Replica, scheduling, autoscaling | K8s Deployment + HPA, Node Pool + Cluster Autoscaler, ASG/MIG |
| `datastore` | Connection limit, cost per jenis query, IOPS, replica | Postgres, MySQL, MongoDB, DynamoDB |
| `cache` | Hit ratio, ops/s per node, memory | Redis, Memcached |
| `queue_stream` | Buffer, partisi, throughput per partisi, consumer lag | Kafka, RabbitMQ, SQS, Pub/Sub |
| `external` | Dependency pihak ketiga dengan latency/limit tetap | Payment gateway, 3rd-party API |

### 5.2 Lapisan yang dipisah dengan jelas
- **Runtime/bahasa** (Go, Rust, …) = properti `compute_service` (pengali CPU kecil + profil GC).
- **Tempat jalan** (EC2, K8s pod) = `runtime_host` / `orchestrator` yang membungkus service.
- **Protokol** (REST, gRPC) = properti edge.

Jadi "Service Go yang jalan di 3 pod K8s" adalah **satu service** yang ditempatkan di dalam satu Deployment, bukan tiga kotak lepas.

### 5.3 Edge (komunikasi)
| Field | Keterangan |
|---|---|
| `protocol` | http1.1+json, http2+json, grpc+protobuf, websocket, async (queue/stream) |
| `calls_per_request` | Fan-out (misal 1 request → 3 call ke service B) |
| `payload_req/resp` | Ukuran rata-rata (KB) → waktu transfer + CPU serialisasi |
| `network_class` | same-host, same-AZ, cross-AZ, cross-region, internet (preset latency) |
| `connection_pool` | Ukuran pool, keep-alive, multiplexing (HTTP/2/gRPC) |
| `timeout`, `retry` (max, backoff, budget), `circuit_breaker` | Kebijakan resiliensi |
| `traffic_split` | Read/write ratio, canary %, routing |

### 5.4 Contoh skema arsitektur (JSON, versi awal)

```json
{
  "version": "0.1",
  "catalogVersion": "2026.10",
  "nodes": [
    { "id": "users", "kind": "client.web", "config": { } },
    { "id": "kong", "kind": "gateway.kong", "config": { "vcpu": 4, "plugins": ["rate-limiting", "key-auth"] } },
    { "id": "pool-a", "kind": "k8s.nodepool", "config": { "instanceType": "c6i.xlarge", "min": 2, "max": 10, "clusterAutoscaler": true } },
    { "id": "orders-deploy", "kind": "k8s.deployment", "parent": "pool-a",
      "config": { "replicas": 3, "requests": { "cpu": "500m", "memory": "512Mi" },
                  "hpa": { "min": 3, "max": 20, "targetCPU": 70 }, "podStartupSec": 20 } },
    { "id": "orders", "kind": "service.go", "parent": "orders-deploy",
      "config": { "profile": "crud-light", "cpuMsPerReq": 4, "overrides": { } } },
    { "id": "pg", "kind": "db.postgres", "config": { "instanceType": "db.r6g.large", "maxConnections": 200, "readReplicas": 1 } },
    { "id": "redis", "kind": "cache.redis", "config": { "hitRatio": 0.85 } }
  ],
  "edges": [
    { "from": "users", "to": "kong", "protocol": "http1.1+json", "networkClass": "internet" },
    { "from": "kong", "to": "orders", "protocol": "http1.1+json", "timeoutMs": 2000 },
    { "from": "orders", "to": "redis", "protocol": "resp", "callsPerRequest": 1 },
    { "from": "orders", "to": "pg", "protocol": "pg-wire", "callsPerRequest": 1, "onlyOn": "cache_miss",
      "retry": { "max": 2, "backoffMs": 50 } }
  ],
  "workload": { "entry": "users", "targetTps": 5000, "shape": "spike", "durationSec": 600,
                "slo": { "p99Ms": 300, "errorRate": 0.001 } }
}
```

---

## 6. Desain Engine Simulasi

### 6.1 Dua mode, satu model komponen

| Mode | Untuk apa | Metode | Kecepatan |
|---|---|---|---|
| **Quick / Analitis** | "Aman di X TPS?", max sustainable TPS, bottleneck steady-state | Jaringan antrian: Utilization Law, Little's Law, M/M/c (Erlang C), koreksi Allen-Cunneen/Kingman untuk service time non-eksponensial | Milidetik → bisa jalan di browser (Go→WASM) setiap kali config berubah |
| **Time-stepped (fluid + sampling)** | Autoscaling, spike, failure, retry storm, timeline | Simulasi diskrit per tick (default 100 ms–1 s); state = laju, antrian, replica; latency percentil dari sampling request sintetis per tick | Detik untuk durasi menit–jam, ribuan–ratusan ribu TPS tetap ringan |

Alasan tidak memakai DES per-request sebagai mode utama: biayanya naik linear dengan TPS (100k TPS × 10 menit = 60 juta event). Fluid + sampling tetap menangkap dinamika penting (antrian, autoscaling, retry amplification) dengan biaya tetap per tick. DES per-request bisa ditambahkan nanti sebagai **mode validasi** untuk skala kecil.

### 6.2 Formula inti (mode analitis)
- Utilisasi: `ρ = λ / (c·μ)` — λ = arrival rate, c = jumlah worker/replica efektif, μ = service rate per worker.
- Probabilitas antre (Erlang C) `C(c, a)` dengan `a = λ/μ`.
- Waktu tunggu: `Wq = C(c,a) / (c·μ − λ)`, total `W = Wq + 1/μ`; Little's Law `L = λ·W`.
- Tail latency: `P(Wq > t) = C(c,a) · e^{−(cμ−λ)t}` → p95/p99 per node.
- Service time non-eksponensial: `Wq ≈ ((ca² + cs²)/2) · Wq(M/M/c)`.
- End-to-end: komposisi per path (serial = jumlah; fan-out paralel = max), percentil e2e via Monte Carlo sampling path.
- **Max sustainable TPS:** binary search λ sampai ada node dengan ρ > threshold (default 0.85) atau SLO terlanggar.

### 6.3 Mode time-stepped
Per tick `Δt`, untuk setiap node (urut topologis, dengan iterasi untuk siklus retry):
1. `λ_in(t)` = trafik masuk (dari upstream × calls_per_request × split) + retry yang dijadwalkan.
2. `μ_eff(t)` = replica **ready** × kapasitas per replica × faktor degradasi (contention di atas ~80% utilisasi, throttling CPU limit, cold start).
3. Antrian: `Q(t+Δt) = clamp(Q + (λ_in − μ_eff)·Δt, 0, Q_max)`; kelebihan Q_max → drop/429/503.
4. Latency ≈ service time + `Q/μ_eff` + overhead edge; request yang melewati timeout → error → **retry** (dengan backoff, budget) → menambah λ upstream (retry storm muncul alami).
5. Circuit breaker: state machine closed/open/half-open per edge berdasar error rate jendela.
6. Metrics per tick: λ, throughput, utilisasi, Q, p50/p95/p99 (dari sampel), error, replica.

### 6.4 Model autoscaling

**K8s HPA** (mengikuti algoritma resmi):
- Loop kontrol berkala, default setiap 15 detik.[13]
- `desiredReplicas = ceil(currentReplicas × currentMetric / desiredMetric)`.[13]
- Tidak melakukan scaling jika rasio dalam toleransi, default 0.1.[13]
- Default scale-down memakai stabilization window 300 detik.[13] Default scale-up tanpa stabilization window, dengan policy 100% atau 4 pod per 15 detik (pilih yang terbesar).[13]
- Ditambah realitas yang sering dilupakan: delay metrics (scrape interval), **pod startup + readiness time**, image pull, dan **pod Pending** jika node pool penuh.

**Cluster Autoscaler / Karpenter:** pod Pending → tambah node setelah delay provisioning (default konfigurabel, misal 60–180 detik) → pod terjadwal.

**VM Autoscaling (ASG/MIG):** target tracking policy, instance warmup, cooldown.

Semua parameter di atas tampil di panel config dengan default yang bisa diubah. Visualisasi timeline: "spike datang di t=60 s → replica naik t=75 s → ready t=95 s → antrian terkuras t=120 s".

### 6.5 Model protokol & edge
- **Latency jaringan** per `network_class` (preset, bisa di-override).
- **Waktu transfer** = payload / bandwidth efektif (beda dengan LOADLINE yang tidak menghitung ini).[7]
- **CPU serialisasi** dibebankan ke kedua ujung (JSON lebih mahal daripada Protobuf; angka default berupa estimasi dan dilabeli).
- **Koneksi:** HTTP/1.1 dibatasi ukuran pool (satu request per koneksi); HTTP/2/gRPC multiplexing; biaya TLS handshake jika tanpa keep-alive.
- **Async:** edge ke queue/stream memutus rantai latency; yang diukur adalah **consumer lag** dan waktu drain.

### 6.6 Model kapasitas per archetype (ringkas)
- **compute_service:** kapasitas per replica ≈ `vCPU_efektif × 1000 / cpuMsPerReq × efisiensi`; `vCPU_efektif` dibatasi CPU limit (throttling). IO wait tidak memakan CPU tapi memakan slot concurrency/koneksi. Profil preset: `crud-light`, `io-bound`, `cpu-heavy`, `custom`. Pengali runtime (Go, Rust, Node, Java, Python) hanya pengali kecil + profil GC — **bukan** penentu utama.
- **gateway (Kong):** kapasitas per vCPU dari benchmark resmi lalu dikurangi per plugin. Contoh sumber: benchmark Kong 3.16 di satu instance c5.4xlarge (16 vCPU, 16 worker process) mencatat sekitar 144k RPS tanpa plugin (p99 4.5 ms) dan sekitar 102k RPS dengan rate limit + key auth (p99 8.45 ms).[14] Default turunannya kurang lebih 9k RPS/vCPU tanpa plugin dan 6.4k RPS/vCPU dengan auth + rate limit, dilabeli "benchmark vendor, kondisi ideal".
- **datastore:** batas koneksi (dan efek connection pool dari semua replica service — sumber bottleneck klasik saat HPA menambah pod), biaya per jenis query (point read, write, complex), IOPS, replica read, lag replikasi.
- **cache:** hit ratio (bisa dinamis saat cold start), ops/s per node, memory → eviction.
- **queue_stream:** partisi × throughput per partisi, jumlah consumer ≤ partisi (Kafka), lag.

### 6.7 Output & verdict
- **Verdict per target TPS:**
  - 🟢 **Aman** — semua node ρ < 70%, SLO terpenuhi dengan margin.
  - 🟡 **Waspada** — ada node 70–85% atau SLO tipis.
  - 🔴 **Overload** — ρ ≥ 85–100%, antrian tumbuh, SLO terlanggar, atau error rate melewati batas.
  - (Threshold bisa diatur user.)
- Bottleneck berurutan + headroom per komponen + "komponen yang jenuh duluan saat TPS dinaikkan".
- p50/p95/p99 end-to-end + breakdown per hop (bagian yang belum ada di LOADLINE).[7]
- Rekomendasi berbasis aturan (fase awal), AI review (fase lanjut, berbayar).

### 6.8 Kejujuran akurasi = fitur
- Setiap parameter punya `source`: `default-estimate` | `vendor-benchmark` (dengan URL) | `user-measured`.
- **Confidence score** per hasil = bobot sumber parameter × **sensitivitas** (parameter yang paling memengaruhi bottleneck paling berpengaruh ke skor).
- Banner jelas: *"Estimasi kasar — 3 parameter kritis masih memakai default. Masukkan hasil load test untuk akurasi lebih baik."*
- Halaman metodologi publik (formula, asumsi, keterbatasan) — LOADLINE sudah mempraktikkan ini dan ini membangun kepercayaan.[7]

---

## 7. Katalog & Default Values

- Disimpan sebagai YAML per komponen di repo (`catalog/`), dengan JSON Schema untuk config → form UI digenerate otomatis.
- **Versi katalog** dikunci per diagram (`catalogVersion`) supaya hasil lama tetap reproducible saat default diperbarui.
- Setiap default menyertakan `source.url`, `source.note`, `confidence`.
- Sumber default: dokumentasi resmi & benchmark vendor (Kong[14], K8s[13]), benchmark framework publik untuk runtime (TechEmpower)[17], dan hasil **lab kalibrasi** sendiri (bagian 9).

Contoh entri:

```yaml
id: gateway.kong
archetype: gateway
displayName: Kong Gateway
version: "3.x"
config:
  vcpu:     { type: number, default: 2, min: 1 }
  workers:  { type: number, default: "=vcpu" }
  plugins:  { type: array, items: [rate-limiting, key-auth, basic-auth, jwt, cors], default: [] }
capacity:
  rpsPerVcpu:
    base:    { value: 9000, source: { url: "https://developer.konghq.com/gateway/performance/benchmarks/", note: "3.16, c5.4xlarge, no plugins, ~144k RPS / 16 vCPU" }, confidence: vendor-benchmark }
    plugins: { rate-limiting: 0.83, key-auth: 0.85 }   # multiplier, estimasi
  latencyP99Ms: { base: 4.5 }
```

### Katalog MVP (usulan)
- **Compute & orkestrasi:** EC2 / GCE / Generic VM, K8s Node Pool, K8s Deployment (+HPA), Cluster Autoscaler, ASG.
- **Service runtime:** Go, Rust, Node.js, Java (JVM), Python, Generic.
- **Edge & gateway:** AWS ALB, Nginx, Kong, Generic API Gateway.
- **Data:** PostgreSQL, MySQL, Redis, MongoDB (opsional).
- **Messaging:** Kafka, RabbitMQ, SQS.
- **Lain:** External API, CDN (sederhana).

---

## 8. Arsitektur Teknis

### 8.1 Gambaran besar

```
┌──────────────────────── Browser ────────────────────────┐
│ React + TS (Vite)                                        │
│  ├─ Canvas: @xyflow/react (React Flow, MIT)              │
│  ├─ State: Zustand · Data: TanStack Query                │
│  ├─ UI: Tailwind + shadcn/ui · Charts: ECharts           │
│  ├─ Forms: generated dari JSON Schema katalog (zod)      │
│  └─ engine.wasm (Go → WASM) → Quick check instan          │
└───────────────┬──────────────────────────────────────────┘
                │ Connect-RPC (proto → TS client) / REST
┌───────────────▼──────────── Go Backend ──────────────────┐
│ api/       : auth, projects, versions, share, billing     │
│ engine/    : model · catalog · sim (quick + time)         │
│              (pure Go, tanpa IO → bisa WASM & CLI)        │
│ jobs/      : antrian run simulasi panjang (river/PG)      │
│ catalog/   : loader + validasi YAML + versi               │
└──────┬──────────────────────┬────────────────────────────┘
       │                      │
   PostgreSQL            Object storage (export PNG/PDF)
```

### 8.2 Keputusan & alasan
- **React Flow (`@xyflow/react`)**: library-nya MIT dan gratis; React Flow Pro hanya memberi akses ke contoh kode pro dan support, bukan versi library terpisah.[15] Mendukung sub-flow/nested node untuk Deployment-di-dalam-NodePool.
- **Engine sebagai paket Go murni** (tanpa IO): satu kode untuk tiga target → server (run panjang), WASM (quick check di browser), CLI (testing & validasi).
- **Connect-RPC + buf**: kontrak tipe-aman Go ↔ TypeScript dari satu file `.proto` (pendekatan yang juga dipakai LOADLINE).[6]
- **PostgreSQL** (pgx + sqlc, migrasi goose); antrian job memakai Postgres (river) dulu — tanpa Redis di awal untuk menekan kompleksitas.
- **Auth:** OAuth Google/GitHub + email magic link (bisa pakai provider terkelola di awal).
- **Billing:** merchant-of-record (Paddle / Lemon Squeezy) supaya pajak internasional diurus provider. Ketersediaan Stripe untuk badan usaha Indonesia perlu dicek dulu.[unverified]
- **Deploy awal:** Docker Compose + Caddy di VM (pola yang sudah dipakai di server sekarang); pindah ke K8s setelah ada traksi.
- **Observability:** OpenTelemetry + Sentry.

### 8.3 Struktur repo (monorepo)

```
archsim/
├─ go.work            # menyatukan module engine + api
├─ apps/
│  ├─ web/            # React + React Flow; engine WASM di Web Worker
│  └─ api/            # Go server: auth single-user + sajikan web (v1.1 #9)
├─ engine/            # Go module: simulasi (pure)
│  ├─ model/          # tipe Architecture, Node, Edge, Workload (+ timeline) + validasi
│  ├─ catalog/        # loader + data YAML (go:embed) — v1.1 #10
│  ├─ sim/            # quick check (M/M/c, Erlang C, Kingman, USL, Monte Carlo, max TPS)
│  │                  # + time simulation (fluid + PSA, HPA/ASG, cluster autoscaler)
│  ├─ examples/       # desain contoh = fixture test + template web
│  ├─ cmd/archsim/    # CLI: archsim run|simulate design.json
│  └─ cmd/wasm/       # entry WebAssembly untuk browser
├─ proto/             # (Fase 3) definisi API
├─ validation/        # lab kalibrasi: compose/kind stacks + k6 scripts
└─ docs/              # metodologi, ADR
```

---

## 9. Strategi Validasi Akurasi

Kredibilitas angka adalah inti produk — kalau angkanya tidak dipercaya, produknya tidak bernilai.

1. **Golden tests matematis:** hasil engine dibandingkan dengan rumus tertutup (M/M/1, M/M/c, Little's Law) — harus cocok dalam toleransi kecil.
2. **Konsistensi antar-mode:** mode analitis vs time-stepped pada steady-state harus konsisten.
3. **Lab kalibrasi (ground truth):** stack referensi nyata (Go service + Postgres + Redis + Kong, di Docker Compose dan kind/K8s) di-load test dengan k6 → bandingkan max sustainable TPS, p99, dan perilaku HPA dengan prediksi.
   - Target awal: prediksi max TPS di stack referensi meleset maksimal ±25%; diperketat seiring kalibrasi.
   - Pengalaman praktisi menunjukkan model sederhana bisa mencapai ±8% di sistem mainan, tapi sulit di production — karena itu kalibrasi dengan data user penting.[12]
4. **Determinisme:** run dengan seed yang sama → hasil identik (penting untuk compare & debugging).
5. **Halaman metodologi publik** + changelog katalog.

---

## 10. Roadmap

Estimasi kasar untuk 1 developer full-time + Samsul sebagai asisten engineering; bisa dipercepat dengan paralelisasi.

### Fase 0 — Spike Engine (2–3 minggu)
- Spesifikasi skema arsitektur v0.1 + 6 archetype inti.
- Engine analitis + CLI (`archsim run design.json`).
- Golden tests vs rumus; 3 desain contoh.
- **Exit:** CLI bisa menjawab "aman di X TPS?", bottleneck, dan max TPS untuk 3 desain contoh; semua golden test hijau.

### Fase 1 — MVP (4–6 minggu)
- Canvas React Flow: palette, nested node, edge dengan protokol, panel config dari JSON Schema.
- Katalog MVP (bagian 7) dengan default bersumber.
- Quick check di browser (WASM) + verdict, bottleneck, heatmap, panel asumsi & confidence.
- Time-stepped minimal: HPA + pola spike, grafik replica & latency (v1.1 #12).
- Simpan lokal + share link (URL hash); auth single-user di server (v1.1 #9).
- **Exit:** 5–10 engineer (beta tester) bisa memodelkan arsitektur kantornya sendiri tanpa bantuan dan menilai hasilnya "masuk akal".

### Fase 2 — Simulasi Waktu & Resiliensi (4–6 minggu)
- Engine time-stepped; HPA, cluster autoscaler, ASG.
- Pola beban (ramp, spike, step, diurnal); failure injection (node/AZ mati, latency injection, dependency down, DB failover).
- Timeout, retry (backoff, budget), circuit breaker → retry storm terlihat.
- Grafik timeline + compare dua versi desain.
- Lab kalibrasi v1 berjalan.
- **Exit:** simulasi HPA cocok secara kualitatif dengan eksperimen di kind + k6 (waktu scale-up, puncak antrian).

### Fase 3 — SaaS Launch (3–4 minggu)
- Akun, workspace, versioning, kuota free vs paid, billing.
- Template gallery (termasuk template belajar/interview sebagai jalur akuisisi).
- Export report PDF; landing page + halaman metodologi; analytics produk.
- **Exit:** launch publik (Product Hunt / HN / komunitas lokal), pembayaran pertama.

### Fase 4 — Growth & Diferensiasi (berkelanjutan)
- **Kalibrasi dari data nyata:** import hasil k6/JMeter/Locust dan metrics Prometheus → auto-tune parameter → confidence naik.
- **Import infra:** Kubernetes YAML / Helm, Docker Compose, Terraform → canvas otomatis.
- Kolaborasi real-time, komentar, review arsitektur.
- Custom component (katalog privat per tim), API publik.
- AI architecture review & rekomendasi.
- (Opsional) estimasi biaya jika diminta pasar.

---

## 11. Monetisasi

Referensi harga pasar: Sketchy Systems US$14.99/bulan[5] dan SysLab Premium $99/tahun.[4] Beberapa pemain lain sepenuhnya gratis.[1][9]

| Paket | Isi | Harga (usulan awal) |
|---|---|---|
| **Free** | 3 desain, Quick check (analitis), katalog dasar, share publik, template | $0 |
| **Pro** (individu) | Desain tak terbatas, simulasi waktu + autoscaling + chaos, compare, export report, kalibrasi manual | ~$12–15/bulan atau ~$120/tahun |
| **Team** | Semua Pro + workspace tim, kolaborasi, versioning & review, katalog privat, import YAML/Terraform, kalibrasi dari metrics, SSO | ~$29–49/user/bulan |
| **Enterprise** (nanti) | Self-host / on-prem, SLA, komponen custom | Kontrak |

Catatan:
- Pertimbangkan harga PPP (regional) untuk pasar Indonesia/Asia Tenggara.
- Fitur paling kuat untuk dijual: **simulasi waktu/autoscale**, **kalibrasi**, **import infra**, **report**, dan **kolaborasi tim**.

---

## 12. Risiko & Mitigasi

| Risiko | Dampak | Mitigasi |
|---|---|---|
| Pasar simulator interview sudah ramai & banyak yang gratis | Sulit menonjol | Positioning capacity-planning production-real; interview hanya jalur akuisisi |
| Akurasi diragukan | Produk tidak dipercaya | Label confidence, metodologi publik, lab kalibrasi, fitur kalibrasi data user |
| Katalog membengkak, sulit dirawat | Lambat menambah fitur | Arsitektur archetype + katalog YAML, versi katalog, kontribusi komunitas |
| Engine kompleks (siklus retry, fan-out, autoscale) | Bug, angka aneh | Engine murni + golden tests + mode konsistensi + seed deterministik |
| Scope creep | MVP tak kunjung rilis | Fase ketat dengan exit criteria; biaya cloud & import di luar MVP |
| Kompetitor open source (LOADLINE dll.) | Tekanan harga | Unggul di autoscale, edge realistis, kalibrasi, UX, dan fitur tim |

---

## 13. Metrik Keberhasilan

- **Aktivasi:** % user baru yang menjalankan ≥ 1 simulasi di sesi pertama.
- **Kualitas:** rating "hasil masuk akal" dari beta tester; selisih prediksi vs lab kalibrasi.
- **Retensi:** user yang kembali dalam 4 minggu.
- **Bisnis:** konversi free → Pro, jumlah tim berbayar.

---

## 14. Keputusan yang Masih Perlu Diambil

1. Nama produk & domain.
2. Prioritas segmen saat launch: individu (learning) dulu atau langsung tim (capacity planning)?
3. Provider auth & billing (merchant-of-record vs payment gateway lokal).
4. Bahasa UI awal: Inggris saja atau Inggris + Indonesia?
5. Open source sebagian (engine) untuk membangun kepercayaan, atau tertutup penuh?
6. Hosting awal: server yang ada sekarang atau terpisah.

---

## 15. Langkah Berikutnya

1. Review plan ini → putuskan poin di bagian 14.
2. Mulai **Fase 0**: inisialisasi repo, skema v0.1, engine analitis + CLI + golden tests.
3. Siapkan 3 desain contoh (REST + Postgres; microservices K8s + Kong; event-driven Kafka) sebagai test fixture sekaligus template.

---

## Sources

[1] https://syssimulator.com — SysSimulator
[3] https://vitabletech.in/blog/paperdraw-system-design-simulator — VitableTech: Deep dive paperdraw.dev
[4] https://syslabs.app/pricing — SysLab pricing
[5] https://sketchysystems.dev — Sketchy Systems
[6] https://github.com/kekubhai/Loadline — LOADLINE (GitHub)
[7] https://raw.githubusercontent.com/kekubhai/Loadline/main/docs/simulation-model.md — LOADLINE simulation model spec
[8] https://github.com/vijaygupta18/system-design-simulator — SystemForge (GitHub)
[9] https://systemdesignsim.com/compare — System Design Sim compare page
[10] https://syde.cc — SyDe
[11] https://dev.to/irishcheezecake/introducing-archscope-visual-system-architecture-simulator-41m2 — ArchScope intro
[12] https://www.reddit.com/r/softwarearchitecture/comments/1uphm0q/systems_design_sim_tool — r/softwarearchitecture: Systems Design Sim Tool?
[13] https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale — Kubernetes HPA docs
[14] https://developer.konghq.com/gateway/performance/benchmarks — Kong Gateway performance benchmarks
[15] https://github.com/xyflow/xyflow/discussions/3484 — xyflow: React Flow free vs Pro
[16] https://www.eraser.io/guides/best-cloud-diagram-tools-in-2026 — Eraser: best cloud diagram tools 2026
[17] https://www.techempower.com/benchmarks — TechEmpower Framework Benchmarks
