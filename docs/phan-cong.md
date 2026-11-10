# Phân công (3 tuần)

Bốn người: **D, N, T, L**. Ai cũng viết code, không phân biệt mức độ. D được dùng Azure
Student + Terraform + Ansible để dựng nhanh relay thử nghiệm.

Đọc cùng [`plan.md`](plan.md) (phạm vi, kiến trúc, định dạng gói tin).

## Nguyên tắc

1. **Mỗi người một vùng thư mục** (bảng ở `plan.md`, mục 4). Không sửa vùng người khác; cần gì
   thì đề nghị đổi hợp đồng (`multiwire` + `contracts.md`).
2. **Không chờ nhau:** D giao hub trong bộ nhớ (`src/multihub` + đường truyền giả) sớm; T, N,
   L thử với nó cho tới khi relay thật xong (mốc M2).
3. **Mỗi mốc có một bài thử chạy tay** bằng CLI, không chỉ "unit test pass". Unit test do
   từng người tự viết cho phần của mình và không nằm trong kế hoạch này.
4. **Mốc chỉ xong khi người khác làm lại được bài thử** theo mô tả, trên máy của họ.

## Vai trò

| Người | Trách nhiệm | Vùng thư mục |
|---|---|---|
| **D** | Hub (logic + gắn vào relay), Azure/Terraform/Ansible, người nhận giả, tăng cứng hub | `src/multihub/`, `src/tcp/multi_room.go` (+1 nhánh trong `tcp.go`), `deploy/`, `tools/multibench/` |
| **T** | Hợp đồng gói tin, PAKE từng người nhận, chuyển `dataKey`, duyệt/khóa phòng ở phía mật mã | `src/multiwire/`, `src/multisession/`, `docs/contracts.md` |
| **N** | Engine người gửi, CLI phía gửi, tiến độ trong terminal, kéo thả đường dẫn, duyệt người vào | `src/multisend/`, `src/cli/multi.go` (phần gửi) |
| **L** | Engine người nhận, CLI phía nhận, benchmark, bài thử LAN thật | `src/multirecv/`, `src/cli/multi.go` (phần nhận), `scripts/bench/`, `docs/multi-lan-test.md` |

`src/cli/multi.go`: N và L viết hàm riêng (gửi/nhận) để không đụng nhau; `src/cli/cli.go`
chỉ thêm đúng một dòng đăng ký, thống nhất ở M0.

---

# Phase 1: MVP chạy được (theo thứ tự thời gian, khoảng ngày 1–10)

```
M0 (cả nhóm) ─┬─ D: M1a ─────────────┐
              ├─ T: M1b ─────────────┤
              ├─ N: M1c ─────────────┼─► M2 (relay thật) ─► M3 (đo tốc độ) ─► M4 (Azure) ─► MVP
              └─ L: M1d ─────────────┘
```

## M0. Khóa hợp đồng (ngày 1–2, T dẫn dắt)

| Ai | Việc | Bài thử chạy tay |
|---|---|---|
| T | Viết lại `docs/contracts.md` từ mục 5 của `plan.md`; tạo `src/multiwire` (mã hóa/giải mã khung, cấu trúc thông điệp) | `go build ./...` thành công; chương trình nhỏ in ra một khung mẫu dạng hex và đọc lại đúng |
| D | Khung `src/multihub` + đường truyền giả trong bộ nhớ; khung `deploy/` | `go run ./tools/multibench --help` chạy được |
| N | Khung `src/multisend`, `src/cli/multi.go` (cờ `--receivers`, `--approve`, `--timeout`) | `croc send --help` hiện cờ mới |
| L | Khung `src/multirecv`, cờ `--multi` | `croc --help` hiện cờ mới |

**Xong khi:** cả 4 chạy được lệnh thử của mình, và cả nhóm đã đọc và đồng ý `contracts.md`.

## M1a. Hub (D)

Việc: `src/multihub`: vai trò SENDER/RECEIVER, `JOIN_ACK`, `DIRECT` (người nhận → người gửi),
`DATA` phát cho mọi người nhận, hàng đợi giới hạn mỗi người, loại người nhận đứng im quá 30
giây, khóa phòng sau N người.

**Bài thử chạy tay:** `go run ./tools/hubdemo` (D viết) chạy 1 người gửi + 3 người nhận trong
cùng tiến trình:
1. Dòng người gửi gửi hiện ở cả 3 người nhận.
2. Người nhận thứ 4 (vượt giới hạn) bị từ chối với thông báo `room-full`/`room-locked`.
3. Một người nhận cố tình không đọc: sau 30 giây bị loại, 2 người còn lại vẫn nhận xong.

## M1b. Vào phòng và trao khóa (T)

Việc: `src/multisession`: PAKE với từng người nhận, mã hóa `session` (manifest + `dataKey`)
bằng khóa cặp, xác nhận bằng `pakekey.Confirm`.

**Bài thử chạy tay:** `go run ./tools/sessiondemo` (T viết), 1 người gửi + 2 người nhận trên hub
giả của D:
1. Code đúng: cả hai in cùng "dấu vân tay phiên" (băm của `dataKey`) với người gửi.
2. Code sai: người nhận báo lỗi rõ ràng, người gửi ghi nhận một lần thử thất bại.
3. In ra luồng khung thô: chỉ thấy bản mã, không đọc được manifest hay tên file.

## M1c. Engine người gửi (N)

Việc: `src/multisend`: liệt kê file (`GetFilesInfoContext`), cắt khối 32 KB, mã hóa một lần bằng
`dataKey`, phát qua hub, chờ `done` của cả N.

**Bài thử chạy tay:** với hub giả và 3 người nhận trong tiến trình:
1. In `3/3 done`.
2. Số byte đã gửi ≈ kích thước file (**không** gấp 3).
3. `sha256` của file ghi ra ở cả 3 nơi giống hệt file gốc.

## M1d. Engine người nhận (L)

Việc: `src/multirecv`: giải mã, ghi qua `receivefs`, kiểm tra băm cuối file, hiển thị tiến độ.

**Bài thử chạy tay:** dùng luồng khung ghi sẵn từ N (hoặc hub giả):
1. File ghi ra khớp `sha256`.
2. Sửa 1 byte trong luồng: người nhận báo "sai băm" và **không để lại file hỏng** như đã xong.
3. Tên file thoát thư mục (`../x`, đường dẫn tuyệt đối) bị từ chối.

## M2. Relay thật (D chủ trì, T/N/L ghép)

Việc: nhánh `multi:` trong `src/tcp/tcp.go` + `src/tcp/multi_room.go`; client dùng
`tcp.ConnectToTCPServerControlContext`; chờ `JOIN_ACK` có giới hạn thời gian.

**Bài thử chạy tay (một máy, 4 terminal):**
1. T1: `croc relay`. T2: `croc send --receivers 3 big.bin` (in code).
2. T3, T4, T5: `croc --multi CODE`. Cả ba nhận được file, `sha256` khớp.
3. Dùng relay cũ (relay công cộng): người gửi báo "relay này không hỗ trợ multi-send" sau vài
   giây, không treo.
4. `croc send file` / `croc CODE` kiểu 1-1 vẫn chạy bình thường trên cùng relay.

## M3. Đo tốc độ (L chủ trì, N hỗ trợ)

Việc: `scripts/bench/run.sh` chạy file 1 GB tới 1, 2, 4, 8 người nhận trên một máy; in thời gian
và số byte người gửi tải lên; chạy kèm một lần `croc` 1-1 cùng file, cùng relay.

**Bài thử chạy tay:** chạy script, dán bảng kết quả vào `docs/multi-lan-test.md`. Đối chiếu
với mục 8 của `plan.md`. Nếu 1 người nhận chậm hơn mục tiêu, mở việc "làn dữ liệu" (Phase 2
Must).

## M4. Relay trên Azure (D)

Việc: `deploy/terraform` dựng 1 VM có IP công khai, mở TCP 9009–9013; `deploy/ansible` cài
`croc` và chạy `croc relay` như dịch vụ.

**Bài thử chạy tay:**
1. `terraform apply` rồi `ansible-playbook` chạy xong không lỗi.
2. Từ hai mạng khác nhau, chạy lại bài thử M2 bằng `--relay <ip>:9009`.

## Mốc MVP: bài thử LAN thật (cả nhóm)

Chi tiết trong `docs/multi-lan-test.md` (L viết khung, cả nhóm điền kết quả).

**Dựng:** 2–3 laptop nối Ethernet cùng một switch (không cần Internet). Một máy chạy
`croc relay`. Chạy `iperf3` giữa từng cặp để ghi tốc độ đường truyền thật (gigabit khoảng
110 MB/s).

**Ma trận chạy:** lần lượt mỗi máy làm người gửi, những máy còn lại nhận:

```
croc send --receivers 2 --relay <ip>:9009 big.bin
croc --multi --relay <ip>:9009 CODE
```

với file 1 GB rồi 2 GB.

**Tiêu chí đạt cho mỗi lượt:**
- `sha256sum` giống nhau trên mọi máy.
- Người gửi chỉ thoát sau khi cả hai máy nhận xong.
- Ghi thời gian, so với tốc độ `iperf3` và với croc 1-1 trên cùng relay.
- Số byte người gửi tải lên ≈ kích thước file.

**Thử lỗi:** rút dây một máy nhận khoảng 50% rồi cắm lại (MVP: máy đó lỗi gọn và người gửi
báo; Phase 2 có vòng bù thì máy đó nhận tiếp); máy thứ ba biết code nhưng vào sau khi đủ
người thì bị từ chối; `croc` 1-1 vẫn chạy.

**Lặp lại một lần qua relay Azure** (mạng Internet thật).

---

# Phase 2: Tính năng bổ sung

Bắt đầu ngay khi M2 chạy được; nhiều việc độc lập nên làm song song với M3/M4.

## Must

| Ai | Việc | Bài thử chạy tay |
|---|---|---|
| N | **Duyệt người vào + khóa phòng:** `--approve` hỏi từng người (IP/tên, dùng `utils.GetInputContext`); khóa phòng sau N người; hết hạn phiên `--timeout` | Hai người nhận vào, người gửi trả lời y/N: người bị từ chối không nhận byte nào; với `--receivers 3`, máy thứ 4 biết code vẫn bị từ chối |
| N | **Kéo thả đường dẫn trong terminal:** chuẩn hóa đường dẫn có dấu nháy, `\ `, `file://`, khoảng trắng cuối, nhiều file kéo cùng lúc | Kéo 3 file từ trình quản lý file vào terminal sau `croc send --receivers 3 `: cả 3 được liệt kê và gửi, kể cả tên có dấu cách |
| N | **Tiến độ từng người trong terminal:** `An 80% \| Bình xong \| Chi đang chờ` và tóm tắt cuối | Quan sát khi chạy 4 người nhận; rút dây một người thì dòng đó hiện "mất kết nối" |
| D + L | **Làn dữ liệu:** thêm kết nối dữ liệu phụ (`multi:<room>#lane<j>`) nếu M3 cho thấy một luồng chậm hơn mục tiêu | Chạy lại benchmark với 1/2/4 làn; chọn mặc định; ghi số trước/sau vào `docs/multi-lan-test.md` |
| L | **Vòng bù / người vào muộn / thử lại:** sau lượt đầu, người nhận báo các đoạn còn thiếu; người gửi phát lại hợp của các đoạn thiếu cho tới khi không ai thiếu ("kết thúc khi người cuối nhận xong"); người nhận mất kết nối được nhận tiếp | Tắt một người nhận ở 50% rồi chạy lại với cùng code: máy đó xong mà các máy khác không bị gửi lại cả file; người gửi chỉ thoát khi cả N xong |
| D | **Tăng cứng hub:** giới hạn kích thước khung, kiểm thử việc loại người nhận chậm, mức log, `--max-rooms` | Một người nhận không bao giờ đọc: bị loại trong 30 giây, các máy khác không bị ảnh hưởng |

## Should

- Gửi thư mục hoàn thiện (bố cục tương đối qua `receivefs`, thư mục rỗng) (N/L).
- `--throttle` ở người gửi (dùng bộ giới hạn có sẵn) cho demo trên LAN (N).
- Tự nhận dạng code multi trong `croc CODE`, bỏ cờ `--multi` ở người nhận (T).
- Trang web cùng luồng, tái sử dụng `web/` (chỉ khi còn dư hơn 3 ngày).

## Could

- `--password` trộn vào bí mật PAKE (chỉ có ích khi gửi qua kênh khác với code; `--code` đã đủ
  cho nhu cầu này); xoay `dataKey` theo file; người nhận gửi tiếp cho nhau; đường trực tiếp
  qua Tailcat.

---

# Lịch gợi ý

| Tuần | D | T | N | L |
|---|---|---|---|---|
| 1 | M0, M1a, M2 | M0, M1b | M0, M1c | M0, M1d |
| 2 | M2 xong, M4 (Azure), tăng cứng hub | Hỗ trợ ghép M2; tự nhận dạng code | Duyệt người vào, kéo thả, tiến độ | M3 (đo), vòng bù |
| 3 | Làn dữ liệu (cùng L), sửa lỗi | Review mật mã, sửa lỗi | Hoàn thiện, `--throttle` | Bài thử LAN thật, biểu đồ benchmark |

Đóng băng tính năng 2 ngày trước hạn; hai ngày đó chỉ chạy lại bài thử LAN và sửa lỗi.

# Rủi ro theo người

- **D:** nhánh `multi:` trong `tcp.go` và loại người nhận chậm là phần dễ sai nhất; giữ nhánh
  nhỏ và test bằng đường truyền trong bộ nhớ trước khi chạm TCP thật.
- **T:** nonce và `dataKey` phải đúng ngay từ đầu; nhờ một người khác review `multisession`
  trước khi ghép M2.
- **N:** `cli.go` chỉ một dòng đăng ký; mọi logic CLI nằm trong `cli/multi.go` để tránh xung
  đột với L.
- **L:** số liệu benchmark phải đo trên mạng thật; chưa có số thì chưa viết số vào tài liệu.
