# Giải thích các tối ưu hiệu năng Gateway

Tài liệu này tóm tắt các thay đổi đã có trong source và điều kiện benchmark gần
45.000 success TPS. Gateway được giữ nguyên giới hạn **1 vCPU / 1 GiB RAM**;
không có bước tăng quota CPU từ 1 lên 4 vCPU.

## Cấu hình benchmark

- Một Gateway, round-robin, năm PDU Session khỏe mạnh.
- HTTP/2 h2c trên cả hai chặng: generator → Gateway và Gateway → PDU.
- Gateway: `GOMAXPROCS=1`, `GOGC=800`, `GOMEMLIMIT=768MiB`; cgroup giới hạn
  cứng 1 vCPU và 1 GiB RAM.
- Generator: `GOMAXPROCS=2`, ba connection × 64 stream, tối đa 192 worker.
- Bài UI mặc định: 50.000 attempts trong cửa sổ 1 giây, yêu cầu tối thiểu
  35.000 HTTP 201, ba lần chạy, nghỉ hai giây giữa các lần. Mỗi connection được
  làm nóng bằng một POST thật trước khi tính giờ; warm-up bổ sung mặc định là 0.
- Request được pacing trong 85% đầu cửa sổ để còn thời gian nhận response.
  Vì thế đây là bài burst có deadline 1 giây, không phải phép đo soak dài.
- Logging được tắt khi benchmark để không tính chi phí ghi mỗi request vào
  throughput của Gateway. `make docker-ui-up PDU_REPLICAS=5` hiện truyền
  `ACCESS_LOG_ENABLED=false LOG_ENABLED=false`.

TPS trên UI là số response HTTP 201 hoàn tất trong cửa sổ chia cho thời gian
cấu hình. `sentRequests` là request được client xác nhận đã ghi lên h2c.
`targetMet` chỉ PASS khi đã lập lịch và gửi đủ target, đồng thời đạt số success
tối thiểu. Một bài có thể vượt ngưỡng success nhưng vẫn FAIL target đầy đủ nếu
chưa gửi kịp toàn bộ request. Percentile chỉ tính trên request thành công và
bao gồm thời gian chờ worker; trung bình p50/p90/p99 qua nhiều lượt không phải
percentile của toàn bộ mẫu gộp.

## Những tối ưu đã thực hiện

### Bộ phát tải và phép đo

- Thay kiểu tạo hàng chục nghìn goroutine cùng lúc bằng fixed worker pool có
  giới hạn theo `connections × streamsPerConnection`.
- Đồng bộ worker bằng start barrier để thời gian dựng goroutine không lọt vào
  cửa sổ đo.
- Pacing theo mốc thời gian tuyệt đối, chia tải thành tối đa 1.000 nhịp để giảm
  timer drift và tránh dồn toàn bộ request vào một thời điểm.
- Đếm riêng attempts, request đã ghi lên socket (`httptrace.WroteRequest`) và
  response HTTP 201 hoàn tất. Cách này không tính một goroutine vừa khởi chạy
  thành request đã gửi.
- Làm nóng connection bằng POST thật trước khi bắt đầu đồng hồ đo.
- Tính latency từ thời điểm job được lên lịch, gồm thời gian đợi worker/stream;
  như vậy latency không bị thấp giả khi queue bắt đầu nghẽn.
- Tái sử dụng transport theo connection và giảm header, context, timer cùng
  string allocation trên đường gửi.

### Gateway và HTTP/2

- Chuyển upstream Gateway → PDU từ HTTP/1.1 sang h2c và reuse connection để
  multiplex nhiều stream, giảm TCP connection churn.
- Giới hạn rõ ràng connection pool và concurrent streams để tránh tăng socket
  hoặc bộ nhớ không giới hạn. Tăng stream quá cao có thể kéo dài queue và tail
  latency, nên cấu hình lớn nhất không mặc nhiên nhanh nhất.
- Thêm buffer ghi cố định 32 KiB trên connection client-facing và upstream
  data-plane của Gateway. Các lần ghi HTTP/2 nhỏ được gom lại; timer 250 µs đẩy
  phần buffer chưa đầy. Lỗi flush đóng connection để đánh thức reader; khi
  đóng connection, dữ liệu đã nhận được drain với deadline 10 ms.
- Tái sử dụng slice value của response header và tránh canonicalization lặp
  khi bỏ hop-by-hop headers.
- Giảm copy buffer của reverse proxy từ 32 KiB xuống 4 KiB cho JSON response
  nhỏ; tái sử dụng deadline/request URL để bớt context, timer và object tạm.

### Discovery, health và xử lý request

- Tách transport data-plane khỏi transport discovery/control-plane để tải
  request không chiếm connection của `/health` và `/metrics`.
- Chỉ health check quyết định backend còn sống. Timeout metrics giữ lại
  `weight`/`activeRequests` gần nhất, không loại PDU khỏi routing pool.
- Yêu cầu ba health-check lỗi liên tiếp trước khi loại backend; health success
  reset bộ đếm lỗi. Việc này tránh một metrics timeout ngắn làm rớt backend khỏe.
- Dùng Sonic để decode request PDU sau khi benchmark microbenchmark xác nhận
  nhanh hơn `encoding/json` và ít allocation hơn; giữ `encoding/json.Encoder`
  cho response vì benchmark của response cho kết quả tốt hơn.

### Runtime và logging

- Khớp `GOMAXPROCS=1` với quota 1 vCPU của Gateway. `GOGC=800` giảm tần suất GC
  để tiết kiệm CPU, đổi lại cho phép heap lớn hơn; `GOMEMLIMIT=768MiB` là giới
  hạn mềm của Go, còn cgroup 1 GiB mới là giới hạn cứng.
- `ACCESS_LOG_ENABLED=false` bỏ access log mỗi request. `LOG_ENABLED=false`
  tắt logger toàn cục. Khi logging bị tắt, wrapper request log trả thẳng
  handler, bỏ tạo request ID, timer và response recorder trên hot path.
- Đây là chế độ benchmark; khi cần chẩn đoán production, bật lại log phù hợp.
  Hai biến phải được truyền lúc tạo/recreate container. Thay biến môi trường
  không đổi được môi trường của container đang chạy.

## Kết quả và giới hạn của tuyên bố 45k TPS

Với log tắt, một Gateway 1 vCPU / 1 GiB và năm PDU:

- 40.000 attempts/1 giây đã hoàn tất 40.000/40.000, không lỗi, trong năm lượt
  liên tiếp.
- 42.000 success TPS trong cửa sổ 5 giây đã hoàn tất 210.000/210.000, không
  lỗi, ở ba lượt.
- 50.000 attempts/1 giây với năm PDU đạt trung bình 41.932 success TPS trong
  ma trận ba lượt; cả ba lượt đều chưa gửi đủ 50.000 trước deadline.
- Mức cao nhất quan sát được là 45.252 success trong một cửa sổ 1 giây khi
  offered load là 80.000 attempts. Lượt đó có 34.748 request timeout/cutoff,
  nên đây là peak quá tải, không phải mức 45k không lỗi.
- Ở tải offered 45.000, một trong bốn lượt đã hoàn tất đủ; ba lượt còn lại
  không đạt đầy đủ. Vì vậy 45k không phải cam kết ổn định.

Kết luận thực tế: **42k TPS là mức không lỗi đã lặp lại được trong bài 5 giây**;
khoảng **45k là peak đã quan sát**, với lỗi khi tiếp tục ép tải. Thêm PDU không
gỡ nút thắt nếu Gateway đã dùng gần hết một core. Trong ma trận, Gateway ở
khoảng 97–98% một core; RAM sử dụng thấp hơn nhiều giới hạn 1 GiB.

Logging ảnh hưởng rõ khi Gateway sát CPU: lúc logging bật, bài UI 50k/1 giây
vừa chạy ba lượt đạt trung bình 30.982 TPS, p50 trung bình 233 ms và Gateway
dùng khoảng 96–97% CPU. Khi logging tắt, các benchmark trước đó đạt khoảng
41–42k ở cùng Gateway quota. Đây là so sánh các lần đo khác thời điểm, chưa
phải A/B xen kẽ trong cùng phiên; kết quả còn dao động theo tải máy, trạng thái
runtime, số PDU và thời điểm chạy.

Các số liệu đầy đủ, điều kiện từng lượt và JSON gốc nằm trong
[`docs/benchmark-35k.md`](docs/benchmark-35k.md) và
[`docs/bao-cao-benchmark-gateway-50k.md`](docs/bao-cao-benchmark-gateway-50k.md).

## Chạy lại UI với 5 PDU và log tắt

```sh
make docker-ui-up PDU_REPLICAS=5
make docker-ui-smoke UI_EXPECTED_BACKENDS=5
```

Sau khi smoke báo đủ năm backend khỏe, mở `http://localhost:18090`, refresh
backends rồi chạy preset. Để so sánh với benchmark lịch sử, giữ nguyên số PDU,
request target, thời lượng, connection/stream, logging và tình trạng tải máy.
Nên lặp nhiều lượt; không lấy riêng lượt cao nhất làm throughput ổn định.
