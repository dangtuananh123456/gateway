# Báo cáo tối ưu Gateway và benchmark 50.000 request/s khi thay đổi số PDU

Ngày đo: **08/10/2026**. Ma trận mới chạy khoảng **17:52–17:54, UTC+7**.

## 1. Kết quả chính

Gateway được giữ ở **1 vCPU / 1 GiB RAM** trong toàn bộ quá trình. Sau tối ưu,
cấu hình 5 PDU đã đạt 40.000 success TPS trong các lần kiểm tra 1 giây và
42.000 success TPS trong ba cửa sổ 5 giây, không lỗi. Đây là kết quả các phép
đo ngắn, không phải bảo đảm throughput cho mọi workload hay một bài soak dài.

Ma trận mới giữ tải **50.000 request trong 1 giây**, thay số PDU theo
**5 → 1 → 3 → 5 → 10 → khôi phục 5**. Mỗi mức chạy ba lần:

| Số PDU | Success TPS trung bình | Khoảng TPS từng lần | p50 TB (ms) | p90 TB (ms) | p99 TB (ms) | Timeout/cutoff TB mỗi lần | CPU Gateway TB |
| ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: |
| **1** | **45.974,3** | 45.670–46.185 | 120,166 | 189,754 | 213,433 | 4.025,7 | 97,32% |
| **3** | **44.220,0** | 42.629–45.063 | 124,118 | 226,597 | 245,484 | 5.780,0 | 97,91% |
| **5** | **41.932,0** | 40.757–43.453 | 150,982 | 253,549 | 283,706 | 8.068,0 | 97,48% |
| **10** | **40.268,3** | 38.295–42.072 | 174,686 | 285,394 | 312,812 | 9.731,7 | 97,36% |

**Cách đọc bảng:** TPS và các cột p50/p90/p99 là trung bình cộng của chỉ số
từng lần chạy. Trung bình ba p90 **không phải** p90 của toàn bộ request gộp lại.
Percentile chính xác của mỗi lần được liệt kê ở mục 5 và lưu trong JSON gốc.
CPU tính từ chênh lệch `cpu.stat.usage_usec` trên thời gian chạy CLI, bao gồm
warmup và phần overhead ngoài cửa sổ đo; đây là mức sử dụng xấp xỉ một core.

Trong ma trận này, **1 PDU đạt throughput cao nhất**, còn tăng PDU lên 10
không giúp xử lý đủ 50k. Tất cả 12 lần đều vượt **35k success TPS**, nhưng
**0/12 lần đạt `targetMet=true`** vì chưa gửi đủ 50.000 request trước cutoff.

## 2. Cấu hình và cách đo

| Thành phần | Cấu hình |
| --- | --- |
| Gateway | Một container; round-robin; HTTP/2 h2c |
| Giới hạn CPU thực tế | `NanoCpus=1000000000`; cgroup `cpu.max=100000 100000` |
| Giới hạn RAM thực tế | `Memory=1073741824` byte, tức 1 GiB |
| RAM + swap cho phép | Tổng tối đa 2 GiB; giới hạn RAM riêng vẫn là 1 GiB |
| Runtime Gateway | `GOMAXPROCS=1`, `GOGC=800`, `GOMEMLIMIT=768MiB` |
| PDU | 1 / 3 / 5 / 10 replica; processing delay 0; store session trong RAM |
| Tài nguyên PDU | Compose chuẩn không đặt quota CPU/RAM; container kiểm tra có `NanoCpus=0`, `Memory=0` |
| Logging | Tắt access log và lifecycle log khi benchmark |
| Endpoint | `POST /nsmf-pdusession/v1/sm-contexts` |
| Tải mỗi lần | 50.000 attempts; cửa sổ đo 1 giây; `min-success=35000` |
| Bộ phát tải | `GOMAXPROCS=2`; 3 connection × 64 stream = 192 worker |
| Request timeout | 3 giây; deadline cửa sổ 1 giây vẫn cắt request sớm hơn |
| Warmup | Một create-session thật trên mỗi connection, không tính vào kết quả |
| Số lần | Ba lần mỗi topology, nghỉ hai giây giữa các lần |
| Môi trường | Go 1.26.5, Linux 7.0.0-34-generic, Docker 29.6.2; host 24 logical CPU |

Gateway giữ nguyên container ID và image trong cả ma trận. Script dùng
`--no-recreate`, đợi `/gateway/backends` trả đúng số backend khỏe rồi mới phát
tải. Scale-down làm các PDU dư bị xóa; scale-up tạo replica mới. Cuối bài đo,
script khôi phục số PDU ban đầu, trong lần này là **5**.

Các chỉ số được tính như sau:

- **TotalRequests:** số attempt đã được lập lịch, gồm cả những job không kịp
  ra socket trước deadline.
- **SentRequests:** số request được xác nhận đã ghi lên connection client h2c
  trong cửa sổ đo. Do quá tải, giá trị này có thể thấp hơn 50.000.
- **Success TPS:** số response HTTP 201 hoàn thành trong cửa sổ chia cho
  thời gian cửa sổ cấu hình. Với cửa sổ 1 giây, TPS bằng số success.
- **FailedRequests:** `TotalRequests - SuccessfulRequests`. Không đồng nghĩa
  với tất cả request đã tới Gateway rồi bị PDU xử lý lỗi.
- **p50/p90/p99:** nearest-rank trên latency của **request thành công**;
  latency tính từ thời điểm request được lên lịch đến khi đọc xong response,
  bao gồm thời gian chờ worker. Request timeout không đóng góp mẫu percentile.
- **`targetMet`:** lập lịch đủ 50.000, gửi đủ 50.000 lên socket trong cửa sổ,
  đồng thời có ít nhất 35.000 success. Chỉ vượt 35k success chưa đủ để cờ này true.

Runner giữ cách pacing hiện có: lập lịch request trong **85% đầu cửa sổ**.
Vì vậy “50k request/s” ở đây là 50k attempts trong cửa sổ 1 giây, không phải
một phép đo arrival rate phẳng ở 50k/s trong thời gian dài. Không đổi pacing,
deadline, payload hay tiêu chí acceptance khi thêm p90.

## 3. Vấn đề ban đầu và kết quả tối ưu

Trước tối ưu, với Gateway 1 vCPU và 5 PDU:

| Workload cũ | Success TPS | Failed | p50 (ms) | p95 (ms) | p99 (ms) |
| --- | ---: | ---: | ---: | ---: | ---: |
| 15k attempts / 1 giây | 15.000 | 0 | 0,878 | 1,898 | 2,408 |
| 50k attempts / 1 giây, yêu cầu 35k success | 21.048 | 28.952 | 292,715 | 621,868 | 631,446 |
| 40k attempts / 1 giây, yêu cầu 35k success | 20.874 | 19.126 | 285,830 | 518,047 | 524,613 |

Các phép đo cũ dùng 15 connection × 32 stream và chưa xuất p90; không thể
suy ngược p90 chính xác từ p50/p95/p99. Các số p90 ở báo cáo này được đo mới.

Profile CPU ban đầu ghi nhận khoảng **26,1% CPU sample ở syscall**, chủ yếu
socket write. HTTP/2 có nhiều HEADERS/DATA write nhỏ, thêm công việc encode/decode
header, cấp phát bộ nhớ, GC, channel/select và lập lịch goroutine. Gateway đã
sử dụng gần trọn một core; tăng PDU lúc đó không gỡ được nút thắt này.

Các thay đổi đã triển khai:

1. **Gom socket write:** buffer cố định 32 KiB trên connection data-plane
   upstream và connection client của Gateway; timer 250 µs flush phần chưa đầy.
   Lỗi flush đóng socket để đánh thức reader. Close cố gắng đẩy dữ liệu đã nhận
   với deadline 10 ms để không bị treo khi peer không đọc.
2. **Giảm cấp phát header:** tái sử dụng value slice của response header, tránh
   canonicalization lặp lại khi xóa các hop-by-hop header có tên đã chuẩn hóa.
3. **Khớp runtime với quota:** `GOMAXPROCS=1` cho Gateway 1 vCPU. Runtime Go hiện
   làm tròn quota thấp hơn hai lên hai Go processor khi tự chọn; cấu hình tường
   minh tránh tình trạng này. `GOGC=800` giảm tần suất GC, đổi thêm RAM lấy CPU.
   `GOMEMLIMIT=768MiB` là soft limit của Go; cgroup vẫn giữ hard limit 1 GiB.
4. **Tuning bộ phát tải:** ba connection × 64 stream và hai Go processor.
   Tăng connection/stream không mặc nhiên tăng throughput khi đã bị giới hạn CPU.

Kết quả tổng hợp có đóng góp của **cả tối ưu source/runtime và tuning bộ phát
tải**. Không quy toàn bộ mức tăng TPS cho riêng một thay đổi source.

Với 5 PDU, các phép đo trước ma trận 50k đã xác nhận:

- Năm lần cuối ở 40k attempts / 1 giây: **40k success, 0 lỗi, targetMet=true**.
- Ba lần ở 42k TPS trong cửa sổ 5 giây: mỗi lần **210.000/210.000 success**, 0 lỗi;
  p99 lần lượt **645,362 / 561,800 / 565,425 ms**.
- Từ khoảng 43k trở lên, một số lần không hoàn thành đủ trước cutoff.

Chi tiết lịch sử, profile và các lượt kiểm tra nằm trong
[benchmark-35k.md](benchmark-35k.md). Các số đo CPU profile trước/sau dùng
workload khác nhau nên tỷ trọng syscall chỉ là bằng chứng chẩn đoán, không phải
một thử nghiệm cô lập riêng hiệu quả batching.

## 4. Tại sao thêm PDU không tăng TPS trong ma trận này?

Điều được **đo trực tiếp** là CPU Gateway ở khoảng **97–98% một core** tại cả
bốn mức PDU. RAM Gateway sau mỗi lượt ở khoảng **43,1–45,9 MiB**, thấp hơn nhiều
so với 1 GiB. Do đó kết quả phù hợp với nút thắt CPU/đường proxy của Gateway,
không phải Gateway thiếu RAM.

Với workload xử lý session nhanh và HTTP/2 multiplexing, một PDU đã phục vụ
được nhiều stream đồng thời. Thêm replica làm Gateway quản lý nhiều upstream
connection, nhiều batch write và probe health/metrics hơn. Chia traffic ra nhiều
pool cũng có thể giảm hiệu quả gom write. Đây là **giải thích suy luận từ source
và số đo**, chưa được xác nhận bằng profile riêng của từng topology.

Không thể kết luận “càng ít PDU càng tốt” cho mọi môi trường: bài đo chỉ có ba
lượt mỗi cấu hình, chạy theo thứ tự tăng replica, không reset Gateway hay store
của PDU sống xuyên suốt. Replica mới và replica cũ có lượng session khác nhau.
Nếu logic PDU nặng hơn, tài nguyên PDU bị giới hạn, hoặc cần availability, kết
quả scale-out có thể khác. Tăng replica còn phục vụ khả năng chịu lỗi chứ không
chỉ throughput của workload nhỏ này.

## 5. Số liệu từng lần ở tải 50k

Tất cả lần chạy có `TotalRequests=50000`, `targetMet=false`; lỗi được runner
phân loại là `timeout`. Status được ghi nhận trong kết quả đều là HTTP 201;
không có HTTP 5xx trong `statusCodes` đã thu thập.

| PDU | Lần | Sent | Success TPS | Failed | p50 (ms) | p90 (ms) | p99 (ms) |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 1 | 45.854 | 45.670 | 4.330 | 99,821 | 174,942 | 210,055 |
| 1 | 2 | 46.236 | 46.068 | 3.932 | 113,698 | 197,955 | 216,554 |
| 1 | 3 | 46.377 | 46.185 | 3.815 | 146,980 | 196,365 | 213,691 |
| 3 | 1 | 42.821 | 42.629 | 7.371 | 134,149 | 250,949 | 272,402 |
| 3 | 2 | 45.255 | 45.063 | 4.937 | 118,649 | 215,458 | 232,267 |
| 3 | 3 | 45.152 | 44.968 | 5.032 | 119,555 | 213,384 | 231,782 |
| 5 | 1 | 43.645 | 43.453 | 6.547 | 142,344 | 232,850 | 257,874 |
| 5 | 2 | 40.949 | 40.757 | 9.243 | 145,721 | 259,433 | 304,721 |
| 5 | 3 | 41.778 | 41.586 | 8.414 | 164,880 | 268,365 | 288,524 |
| 10 | 1 | 42.264 | 42.072 | 7.928 | 153,236 | 257,222 | 283,325 |
| 10 | 2 | 38.481 | 38.295 | 11.705 | 206,405 | 314,696 | 343,255 |
| 10 | 3 | 40.587 | 40.438 | 9.562 | 164,417 | 284,263 | 311,855 |

Tỷ lệ thành công trung bình: **91,95% / 88,44% / 83,86% / 80,54%** tương ứng
1 / 3 / 5 / 10 PDU. Tăng tải vượt công suất làm queue dài hơn và tăng tail latency.
Percentile chỉ trên success vì vậy phải đọc cùng tỷ lệ lỗi, không dùng riêng
p99 để kết luận toàn bộ workload đáp ứng tốt.

## 6. Goroutine, bộ nhớ và đánh đổi

Project quản lý lifecycle bằng context cancellation, timeout, worker pool có
giới hạn, WaitGroup, shutdown có deadline, đóng response body và idle connection.
Các package lifecycle quan trọng còn có `goleak` và endpoint pprof.

Sau các bài đo trước, pprof trở về khoảng 24 goroutine. Sau ma trận mới và thời
gian idle, pprof ghi **19 goroutine**, RAM container khoảng **43,14 MiB**; số
goroutine thay đổi theo connection pool và hoạt động probe. Không thấy bằng
chứng tăng goroutine liên tục trong các phép đo này, nhưng chưa thay thế một
bài soak nhiều giờ. `memory.peak` trong JSON là peak cả đời container, không
phải peak riêng của từng lượt chạy.

Batching có thể thêm độ trễ nhỏ ở tải thấp; timer 250 µs không phải bảo đảm trần
latency khi CPU bận. GC thưa hơn dùng thêm RAM. Benchmark 15k cũ sau tối ưu vẫn
pass không lỗi, nhưng p50/p95/p99 là 2,007/3,250/4,582 ms, cao hơn lần baseline.

## 7. Khuyến nghị theo bằng chứng hiện có

- Với 5 PDU và Gateway hiện tại, dùng khoảng **40k TPS** làm mức kiểm tra có
  khoảng dư; 42k đã pass các cửa sổ ngắn nhưng p99 ở cửa sổ 5 giây tăng rõ.
- Nếu yêu cầu **50k success/s và không lỗi**, chưa đạt ở cả bốn topology đã đo.
  Chỉ tăng PDU không đủ; cần kiểm tra thêm CPU/replica Gateway hoặc tiếp tục tối
  ưu protocol/proxy. Chưa có số đo cho Gateway 2 vCPU trong báo cáo này.
- Nếu SLA là **ít nhất 35k success/s**, cả 12 lượt 50k đều vượt mức đó, nhưng
  vẫn có timeout và `targetMet=false`; không được gọi là pass đầy đủ 50k.
- Không giảm production xuống một PDU chỉ vì kết quả TPS: cần tính cả redundancy
  và tải thực tế. Một PDU đứng đầu trong bài này không chứng minh nó đủ cho SLA
  availability của hệ thống.

## 8. Chạy lại và truy xuất dữ liệu

Đã thêm `latencyP90Millis` vào kết quả CLI và phép aggregate, giữ nguyên p95.
Test kiểm tra p90 của dãy 1–100 ms bằng 90 ms và không tính response sau deadline.

Chạy lại toàn bộ ma trận từ repo, khi các service đang chạy:

```sh
bash scripts/benchmark-pdu-matrix.sh docs/benchmarks/50k-pdu-rerun
```

Script build CLI từ source, scale lần lượt 1/3/5/10 PDU, chờ đúng membership,
chạy ba lượt mỗi mức và khôi phục số PDU ban đầu qua EXIT trap.

Chạy riêng một lượt trên topology hiện tại:

```sh
GOMAXPROCS=2 go run ./cmd/loadtest \
  -target http://localhost:18080/nsmf-pdusession/v1/sm-contexts \
  -requests 50000 -min-success 35000 -duration 1s \
  -connections 3 -streams 64 -request-timeout 3s
```

Dữ liệu của báo cáo:

- [Environment thực tế](benchmarks/50k-pdu-2026-10-08/environment.json).
- [Tổng hợp máy đọc được](benchmarks/50k-pdu-2026-10-08/summary.json).
- Các file `pdu-N-run-R.result.json`: kết quả CLI gốc, gồm p50/p90/p95/p99,
  status và error counters.
- Các file `pdu-N-run-R.metadata.json`: thêm thời điểm UTC, topology, CPU và RAM.
- Các file `pdu-N-run-R.stderr.log`: thông báo acceptance target không đạt.

Code liên quan: `internal/loadtest/runner.go`, `internal/gateway/batch_conn.go`,
`docker-compose.yml` và `scripts/benchmark-pdu-matrix.sh`. Kiểm tra gồm race tests,
vet, format, syntax Bash và xác nhận topology đã khôi phục về **1 Gateway + 5 PDU**.

## 9. Bổ sung: tăng từng replica 5 → 6 → 7

Theo yêu cầu tăng từ từ, chạy một lượt ma trận mới theo đúng thứ tự
**5 → 6 → 7**, mỗi bước thêm **một** PDU. Đợi membership khỏe đúng số replica
rồi chạy ba lần ở mỗi mức. Gateway giữ nguyên container, quota 1 vCPU/1 GiB
và runtime; workload vẫn 50.000 attempts / 1 giây, ba connection × 64 stream,
generator `GOMAXPROCS=2`. Cuối cùng khôi phục về 5 PDU.

Baseline 5 PDU được **đo lại trong lượt này**, không lấy số trung bình 5 PDU
của ma trận 1/3/5/10 làm baseline cho 6 và 7.

| PDU | Success TPS TB | Khoảng TPS | p50 TB (ms) | p90 TB (ms) | p99 TB (ms) | Failed TB | CPU Gateway TB |
| ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: |
| 5 | **42.596,0** | 42.335–42.955 | 147,058 | 249,719 | 273,502 | 7.404,0 | 97,68% |
| 6 | **41.453,7** | 39.324–44.126 | 155,785 | 271,770 | 292,438 | 8.546,3 | 97,17% |
| 7 | **41.692,3** | 40.796–42.281 | 145,814 | 264,671 | 288,864 | 8.307,7 | 96,99% |

TB là trung bình chỉ số của ba lần; các percentile trung bình không phải
percentile gộp của toàn bộ request. CPU bao gồm thời gian warmup/overhead như
phương pháp ở mục 1. Số liệu riêng từng lần:

| PDU | Lần | Sent | Success TPS | Failed | p50 (ms) | p90 (ms) | p99 (ms) |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5 | 1 | 43.125 | 42.955 | 7.045 | 131,607 | 245,196 | 266,596 |
| 5 | 2 | 42.527 | 42.335 | 7.665 | 145,284 | 255,013 | 277,415 |
| 5 | 3 | 42.690 | 42.498 | 7.502 | 164,283 | 248,947 | 276,496 |
| 6 | 1 | 44.283 | 44.126 | 5.874 | 130,863 | 226,607 | 245,718 |
| 6 | 2 | 39.491 | 39.324 | 10.676 | 162,939 | 312,537 | 330,452 |
| 6 | 3 | 41.091 | 40.911 | 9.089 | 173,552 | 276,165 | 301,145 |
| 7 | 1 | 40.988 | 40.796 | 9.204 | 147,855 | 275,754 | 303,305 |
| 7 | 2 | 42.189 | 42.000 | 8.000 | 150,578 | 261,097 | 283,965 |
| 7 | 3 | 42.473 | 42.281 | 7.719 | 139,009 | 257,161 | 279,323 |

Kết quả: cả chín lần đều vượt 35k success TPS, nhưng **0/9 đạt `targetMet`**
vì chưa gửi đủ 50.000 trước deadline. Tăng từng bước lên 6 và 7 không cho thấy
cải thiện throughput rõ so với baseline 5 trong lượt này; các khoảng TPS còn
chồng lấn nên không suy ra một quy luật giảm/tăng tuyệt đối từ ba lần đo.
Gateway vẫn sử dụng khoảng 97% một core, phù hợp với nút thắt CPU đã quan sát.

Script đã hỗ trợ truyền danh sách replica để chạy lại từng bước:

```sh
bash scripts/benchmark-pdu-matrix.sh \
  docs/benchmarks/50k-pdu-incremental-rerun 5 6 7
```

Nếu không truyền danh sách, script vẫn dùng ma trận mặc định 1/3/5/10.
Lượt đo này lưu [environment](benchmarks/50k-pdu-incremental-2026-10-08/environment.json),
[summary](benchmarks/50k-pdu-incremental-2026-10-08/summary.json) và JSON/log từng
lần trong thư mục `docs/benchmarks/50k-pdu-incremental-2026-10-08`.

## 10. Tự kiểm tra bằng UI client

UI mới chạy tại **http://localhost:18090**. Client container đặt `GOMAXPROCS=2`,
Gateway và các PDU hiện có không bị recreate khi khởi chạy UI.

Các chức năng đã bổ sung:

- Preset **50k / ≥35k success**, **40k / ≥35k success**, và **15k / ≥12k success**.
- Nhập tổng request, success tối thiểu, số lần, thời gian cửa sổ, connection,
  stream, timeout, pause giữa lượt và warmup bổ sung.
- Hiển thị **p50/p90/p95/p99**, attempted/sent, success/failed.
- Hai trạng thái độc lập: **Ngưỡng success** và **Target đầy đủ**. Đạt ≥35k
  nhưng chưa gửi đủ target vẫn hiển thị ĐẠT ở cột success và FAIL ở cột target.
- Số PDU healthy hiện tại và số PDU trước/sau bài đo; cảnh báo trong kết quả
  nếu count hoặc routing mode đổi giữa hai snapshot.
- Tải JSON của kết quả vừa chạy, có environment, error/status counters và p90.

Cách dùng để so sánh 5 → 6 → 7:

1. Refresh backends và đợi đủ 5 PDU, chọn preset 50k, nhấn **Run E2E test**.
2. Tải JSON để lưu kết quả của topology 5.
3. Chọn 6 ở vùng backend, nhấn **Copy lệnh scale**, chạy lệnh đó trong terminal.
   Dropdown/copy không tự điều khiển Docker từ browser.
4. Refresh đến khi đủ 6 PDU rồi chạy lại; thực hiện tương tự với 7. Không scale
   trong lúc một bài benchmark đang chạy.

Tổng request là số lượng của **cả cửa sổ**. Ví dụ 50.000 request và cửa sổ 5s
là offered load trung bình 10k/s; muốn thử 50k/s trong 5s cần nhập 250.000.
Mỗi lượt vẫn warm connection bằng request thật ngay cả khi warmup bổ sung là 0.
Pause mặc định hai giây giữa các lần để gần với cách chạy ma trận CLI.

Kiểm tra thật qua API UI trên 5 PDU:

| Bài đo | Success TPS | p50 (ms) | p90 (ms) | p99 (ms) | Ngưỡng success | Target đầy đủ |
| --- | ---: | ---: | ---: | ---: | --- | --- |
| 50k/35k, trung bình 3 lần | 41.438,7 | 140,807 | 270,273 | 291,891 | Đạt mọi lần | FAIL mọi lần |
| 40k/35k, 1 lần | 40.000 | 78,779 | 125,488 | 139,217 | Đạt | PASS |

Các cột percentile của dòng trung bình vẫn là TB chỉ số từng lượt, không phải
percentile gộp. Cả hai bài ghi nhận PDU trước/sau là **5 → 5**.

UI dùng cùng `internal/loadtest.Runner` và cấu hình workload, nhưng bộ phát tải
chạy trong container client và gọi `http://gateway:8080` qua Docker network.
CLI trước đó gọi published port `http://localhost:18080` từ host. Đường mạng và
thời điểm đo khác nhau nên không yêu cầu TPS/latency trùng chính xác từng số.

Dữ liệu kiểm tra: [50k.json](benchmarks/ui-client-2026-10-08/50k.json) và
[40k.json](benchmarks/ui-client-2026-10-08/40k.json).
Race tests, vet, format, OpenAPI JSON, JS syntax, Go UI smoke và JS contract
smoke đều pass. Phiên kiểm tra không có browser kết nối nên chưa thực hiện
visual/layout QA; JS smoke dùng DOM stub, không được tính là browser E2E test.

```sh
ACCESS_LOG_ENABLED=false LOG_ENABLED=false docker compose --profile tools up --build -d --no-deps client
go run ./scripts/ui-smoke.go http://localhost:18090 5
node scripts/ui-performance-smoke.mjs docs/benchmarks/ui-client-2026-10-08/50k.json
```
