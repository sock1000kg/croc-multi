# Phân công (3 tuần)

Bốn người: **D, N, T, L**. Ai cũng viết code, không phân biệt mức độ. D được dùng Azure
Student + Terraform + Ansible để dựng nhanh relay thử nghiệm.

Đọc cùng [`plan.md`](plan.md) (phạm vi, kiến trúc, định dạng gói tin) và
[`contracts.md`](contracts.md) (khung, thông điệp, chữ ký hàm).

## Nguyên tắc

1. **Mỗi người một vùng thư mục** (bảng ở `plan.md`, mục 4). Không sửa vùng người khác; cần gì
   thì đề nghị đổi hợp đồng (`multiwire` + `contracts.md`).
2. **Không chờ nhau:** D giao hub trong bộ nhớ (`src/multihub` + đường truyền trong bộ nhớ) sớm; T, N,
   L thử với nó cho tới khi relay thật xong (mốc M2).
3. **Mỗi việc có hai phần:** *Cách làm* (gợi ý ngắn, có thể đổi nếu thấy tốt hơn) và *Kiểm
   chứng* (bài thử chạy tay bằng CLI, không chỉ "unit test pass"). Unit test do từng người tự
   viết và không nằm trong kế hoạch này.
4. **Mốc chỉ xong khi người khác làm lại được bài thử** theo mô tả, trên máy của họ.
5. **Không cần gặp mặt.** Mọi bài thử cuối dùng relay trên Azure (hoặc VPS) và chạy từ nhà mỗi
   người; một máy có thể chạy nhiều người nhận bằng nhiều terminal/thư mục.

## Vai trò

| Người | Trách nhiệm | Vùng thư mục |
|---|---|---|
| **D** | Hub (logic + gắn vào relay), Azure/Terraform/Ansible, người nhận giả, tăng cứng hub | `src/multihub/`, `src/tcp/multi_room.go` (+1 nhánh trong `tcp.go`), `deploy/`, `tools/multibench/` |
| **T** | Hợp đồng gói tin, PAKE từng người nhận, chuyển `dataKey`, duyệt người vào ở phía mật mã | `src/multiwire/`, `src/multisession/`, `docs/contracts.md` |
| **N** | Engine người gửi, CLI phía gửi, tiến độ trong terminal, kéo thả đường dẫn, duyệt người vào | `src/multisend/`, `src/cli/multi.go` (phần gửi) |
| **L** | Engine người nhận, CLI phía nhận, benchmark, bài thử đầu-cuối | `src/multirecv/`, `src/cli/multi.go` (phần nhận), `scripts/bench/`, `docs/multi-e2e-test.md` |

`src/cli/multi.go`: N và L viết hàm riêng (gửi/nhận) để không đụng nhau; `src/cli/cli.go`
chỉ thêm đúng một dòng đăng ký, thống nhất ở M0.

---

# Phase 1: MVP chạy được (tuần 1, ngày 1–7)

**MVP = người gửi đẩy file qua relay Azure, 3 người nhận ở 3 nơi nhận đủ, `sha256` khớp.**
Phải xong cuối tuần 1; tuần 2–3 chỉ là đo đạc và tính năng bổ sung. Nếu trễ, cắt Phase 2
chứ không dời MVP.

```
Ngày 1     Ngày 2-4                 Ngày 5-6        Ngày 7
M0 ──┬─ D: M1a ───────────┐
     ├─ T: M1b ───────────┤
     ├─ N: M1c ───────────┼─► M2 (relay thật) ─► M4 (Azure) ─► MVP (thử từ xa)
     └─ L: M1d ───────────┘
D còn làm khung deploy/ + Terraform song song từ ngày 1 (không phụ thuộc code hub).
M3 (đo tốc độ) bắt đầu ngay khi M2 chạy, làm sang tuần 2.
```

## M0. Khóa hợp đồng (ngày 1, T dẫn dắt)

| Ai | Việc | Bài thử chạy tay |
|---|---|---|
| T | Rà soát và chốt `docs/contracts.md` (bản khởi đầu đã có; giải quyết các điểm ở mục 8 của nó); tạo `src/multiwire` (mã hóa/giải mã khung, cấu trúc thông điệp) | `go build ./...` thành công; chương trình nhỏ in ra một khung mẫu dạng hex và đọc lại đúng |
| D | Khung `src/multihub` + đường truyền trong bộ nhớ trong bộ nhớ; khung `deploy/` | `go run ./tools/multibench --help` chạy được |
| N | Khung `src/multisend`, `src/cli/multi.go` (cờ `--receivers`, `--approve`, `--timeout`) | `croc send --help` hiện cờ mới |
| L | Khung `src/multirecv`, cờ `--multi` | `croc --help` hiện cờ mới |

**Xong khi:** cả 4 chạy được lệnh thử của mình, và cả nhóm đã đọc và đồng ý `contracts.md`.

## M1a. Hub (D)

**Việc:** `src/multihub`: vai trò SENDER/RECEIVER, `JOIN_ACK`, `DIRECT` (người nhận → người
gửi), `DATA` phát cho mọi người nhận, hàng đợi giới hạn mỗi người, loại người nhận đứng im
quá 30 giây, đuổi thành viên theo lệnh `KICK` của người gửi, khóa khi có `DATA` đầu tiên.

**Cách làm:**
- Gói thuần Go, không import `net`: nhận `multihub.Conn` (đọc/ghi khung) nên chạy được với
  `net.Pipe` hoặc kênh trong bộ nhớ.
- Mỗi phòng một goroutine đọc từ người gửi; mỗi người nhận một goroutine ghi, đọc từ hàng
  đợi có giới hạn (~8 MB, đếm theo byte). Hàng đợi đầy thì `DATA` của người gửi bị chặn
  (đó chính là backpressure; không cần cơ chế riêng).
- Mỗi người nhận có đồng hồ "lần cuối ghi được"; goroutine ghi dùng `SetWriteDeadline` 30 giây,
  hết hạn thì gửi `ERROR kicked-slow`, đóng, báo `MEMBER_LEFT` cho người gửi.
- Hub chỉ đọc phần đầu khung (`type/from/to`), không giải mã payload.

**Kiểm chứng:** `go run ./tools/hubdemo` (D viết) chạy 1 người gửi + 3 người nhận trong cùng
tiến trình:
1. Dòng người gửi gửi hiện ở cả 3 người nhận.
2. Người nhận vượt `--multi-max-receivers` bị từ chối `room-full`; người gửi `KICK` một người
   thì kết nối đó đóng ngay dù client cố giữ; 4 người cùng một IP đều vào và nhận được bình thường dù trước đó có người bị `KICK`; vào sau `DATA` đầu tiên thì nhận `room-locked`.
3. Một người nhận cố tình không đọc: sau 30 giây bị loại, 2 người còn lại vẫn nhận xong.

## M1b. Vào phòng và trao khóa (T)

**Việc:** `src/multisession`: PAKE với từng người nhận, mã hóa `session` (manifest + `dataKey`)
bằng khóa cặp, xác nhận bằng `pakekey.Confirm`.

**Cách làm:**
- Theo `contracts.md` mục 3: người nhận là bên A, người gửi là bên B, dùng
  `pakekey.Init(passphrase, role, "p256", PurposeTransfer, "multi:"+RoomName+"/"+memberID)`.
- Người gửi giữ một bản đồ `memberID → trạng thái PAKE`; mỗi lần có `MEMBER_JOINED` thì mở
  một goroutine xử lý riêng người đó, nên một người nhập sai code không làm kẹt người khác.
- `dataKey` = 32 byte từ `crypto/rand`, sinh một lần mỗi phiên; chỉ gửi trong `session`
  đã mã hóa bằng khóa cặp của người đó.
- Cung cấp móc `AuthorizeReceiver` để N gắn việc duyệt người vào sau này.

**Kiểm chứng:** `go run ./tools/sessiondemo` (T viết), 1 người gửi + 2 người nhận trên hub
giả của D:
1. Code đúng: cả hai in cùng "dấu vân tay phiên" (băm của `dataKey`) với người gửi.
2. Code sai: người nhận báo lỗi rõ ràng, người gửi ghi nhận một lần thử thất bại, người nhận
   còn lại không bị ảnh hưởng.
3. In ra luồng khung thô: chỉ thấy bản mã, không đọc được manifest hay tên file.

## M1c. Engine người gửi (N)

**Việc:** `src/multisend`: liệt kê file (`GetFilesInfoContext`), cắt khối 32 KB, mã hóa một lần
bằng `dataKey`, phát qua hub, chờ `done` của cả N.

**Cách làm:**
- Một vòng đọc tuần tự mỗi file → gói thành bản ghi `kind 1` (`fileIdx | offset | data`) →
  `crypt.EncryptAEADTo` bằng `dataKey` → `Conn.Write` khung `DATA`. Cuối file gửi `kind 2`
  (sha256 của file), cuối phiên gửi `kind 3`.
- Băm file bằng `io.TeeReader` ngay lúc đọc để không đọc file hai lần.
- Đếm byte ghi lên hub để in "đã gửi X MB" (kiểm tra không nhân N).
- Chờ `done` từ mọi `memberID` qua `DIRECT`; `fail` hoặc `MEMBER_LEFT` thì ghi nhận người đó
  lỗi và vẫn chờ những người còn lại.

**Kiểm chứng:** với hub trong bộ nhớ và 3 người nhận trong tiến trình:
1. In `3/3 done`.
2. Số byte đã gửi ≈ kích thước file (**không** gấp 3).
3. `sha256` của file ghi ra ở cả 3 nơi giống hệt file gốc.

## M1d. Engine người nhận (L)

**Việc:** `src/multirecv`: giải mã, ghi qua `receivefs`, kiểm tra băm cuối file, hiển thị tiến
độ.

**Cách làm:**
- Vòng đọc khung `DATA` → `crypt.DecryptAEADFrom` bằng `dataKey` → theo `fileIdx` ghi vào tệp
  tạm do `receivefs` mở; đến `kind 2` so sánh sha256 rồi mới đổi tên thành tên cuối.
- Sai băm: xóa tệp tạm, gửi `fail` cho người gửi, thoát với mã lỗi khác 0.
- Tên file lấy từ manifest và chỉ đi qua `receivefs` (nó đã chặn `..`, đường dẫn tuyệt đối);
  không tự ghép đường dẫn.
- Tiến độ in một dòng, cập nhật bằng `\r` hoặc `termui`.

**Kiểm chứng:** dùng luồng khung ghi sẵn từ N (hoặc hub trong bộ nhớ):
1. File ghi ra khớp `sha256`.
2. Sửa 1 byte trong luồng: người nhận báo "sai băm" và **không để lại file hỏng** như đã xong.
3. Tên file thoát thư mục (`../x`, đường dẫn tuyệt đối) bị từ chối.

**M1 xong khi** (cuối ngày 4): `multisend` + `multirecv` + `multisession` thật chạy với nhau trên
hub trong bộ nhớ của D: 1 người gửi, 2 người nhận, file khớp `sha256`. Ai chưa ghép được thì sửa
trước khi sang M2.

## M2. Relay thật (D chủ trì, T/N/L ghép; ngày 5–6)

**Việc:** nhánh `multi:` trong `src/tcp/tcp.go` + `src/tcp/multi_room.go`; client dùng
`tcp.ConnectToTCPServerControlContext`; chờ `JOIN_ACK` có giới hạn thời gian.

**Cách làm:**
- Trong `clientCommunication`, sau khi giải mã tên phòng: nếu bắt đầu bằng `multi:` thì giao
  `net.Conn` cho `multi_room.go` (bọc bằng `comm.Comm` rồi chuyển thành `multihub.Conn`),
  ngược lại chạy đường cũ không đổi một dòng nào.
- Cờ relay: `--multi-max-receivers`, `--multi-queue-mb`, `--multi-stall-timeout`,
  `--multi-join-rate` (chống lũ kết nối, mặc định 120/phút/IP).
- Hub **không** chặn hay đếm theo IP: người dùng chung NAT phải vào được bình thường.
- Phía client: sau `ConnectToTCPServerControlContext` gửi `JOIN`, chờ `JOIN_ACK` tối đa 5 giây;
  quá hạn thì báo "relay này không hỗ trợ multi-send, hãy dùng `croc relay` của bạn".
- Cả nhóm ghép: N nối `multisend` vào `multisession`, L nối `multirecv`, T nối cả hai vào
  `multiwire` thật thay hub trong bộ nhớ.

**Kiểm chứng (một máy, 5 terminal):**
1. T1: `croc relay`. T2: `croc send --receivers 3 big.bin` (in code).
2. T3, T4, T5: `croc --multi CODE`. Cả ba nhận được file, `sha256` khớp.
3. Dùng relay cũ (relay công cộng): người gửi báo "relay này không hỗ trợ multi-send" sau vài
   giây, không treo.
4. `croc send file` / `croc CODE` kiểu 1-1 vẫn chạy bình thường trên cùng relay.

## M4. Relay trên Azure (D; khung làm từ ngày 1, chạy thật ngày 6)

**Việc:** `deploy/terraform` dựng 1 VM có IP công khai, mở TCP 9009–9013; `deploy/ansible` cài
`croc` và chạy `croc relay` như dịch vụ.

**Cách làm:**
- Terraform: resource group, VM nhỏ (Student đủ dùng), IP tĩnh, NSG mở 22 và 9009–9013 TCP,
  xuất `public_ip` ra output.
- Ansible: cài Go, `git clone` repo `croc-multi` đúng nhánh, `go build ./...`, tạo file
  `systemd` chạy `croc relay --ports 9009,9010,9011,9012,9013 --pass <mật khẩu relay>`,
  bật khởi động cùng máy. Triển khai lại = `git pull` + build + restart (một playbook).
- Lưu mật khẩu relay và IP trong tệp biến không đưa vào git (`.gitignore`).

**Kiểm chứng:**
1. `terraform apply` rồi `ansible-playbook` chạy xong không lỗi.
2. `nc -vz <ip> 9009` từ máy bất kỳ báo mở.
3. Chạy lại bài thử M2 bằng `--relay <ip>:9009 --pass <mật khẩu>` từ máy của D, rồi từ máy
   của một người khác ở mạng khác.

## M3. Đo tốc độ (L chủ trì, N hỗ trợ; bắt đầu khi M2 chạy, xong trong tuần 2)

**Việc:** `scripts/bench/run.sh` chạy file 1 GB tới 1, 2, 4, 8 người nhận; in thời gian và số
byte người gửi tải lên; chạy kèm một lần `croc` 1-1 cùng file, cùng relay.

**Cách làm:**
- Script tạo file bằng `head -c 1G /dev/urandom`, khởi động `croc relay` trên cổng riêng,
  chạy người gửi và k người nhận nền với `--out` khác nhau, đo bằng `date +%s.%N`.
- Đọc số byte người gửi từ dòng tổng kết của `multisend` (đã in ở M1c); với croc 1-1 dùng
  thời gian tường.
- Chạy hai kiểu: một máy (relay `localhost`, đo giới hạn CPU/mã hóa) và qua relay Azure (đo
  băng thông thật). Chỉ kiểu thứ hai mới đưa vào kết luận.

**Kiểm chứng:** chạy script, dán bảng kết quả vào `docs/multi-e2e-test.md`. Đối chiếu với mục
8 của `plan.md`. Nếu 1 người nhận chậm hơn mục tiêu, mở việc "làn dữ liệu" (Phase 2 Must).

## Mốc MVP: bài thử đầu-cuối từ xa (cả nhóm, ngày 7)

Không cần gặp mặt, không cần cùng mạng. Chi tiết và bảng kết quả ở `docs/multi-e2e-test.md`
(L viết khung, mỗi người điền phần của mình).

**Dựng:** relay Azure của D chạy sẵn (M4). Mỗi người cài bản `croc` build từ nhánh chung. Họp
voice/chat để phối hợp lệnh, nhưng mỗi người chạy ở máy mình.

**Ma trận (mỗi dòng ≈ 5 phút):**

| Lượt | Người gửi | Người nhận | File |
|---|---|---|---|
| 1 | D | N, T, L | 100 MB |
| 2 | N | D, T, L | 100 MB |
| 3 | T | D, N, L | 1 GB |
| 4 | L | D, N, T | 1 GB |
| 5 | D | 8 người nhận: 4 máy × 2 terminal | 1 GB |

```
croc send --receivers 3 --relay <ip>:9009 --pass <mật khẩu> big.bin
croc --multi --relay <ip>:9009 --pass <mật khẩu> CODE
```

**Tiêu chí đạt cho mỗi lượt:**
- `sha256sum` giống nhau trên mọi máy.
- Người gửi chỉ thoát sau khi mọi người nhận xong.
- Ghi thời gian và băng thông upload của người gửi (đo bằng `speedtest` hoặc `iperf3` tới
  relay trước); số byte người gửi tải lên ≈ kích thước file, không nhân với số người nhận.

**Thử lỗi (dùng `Ctrl+C`, tắt Wi‑Fi, hoặc kill tiến trình thay cho việc rút dây):**
- Tắt một máy nhận ở khoảng 50% (MVP: máy đó lỗi gọn và người gửi báo; Phase 2 có vòng bù thì
  máy đó nhận tiếp).
- Máy thứ tư biết code nhưng vào sau khi đủ người thì bị từ chối.
- `croc` 1-1 vẫn chạy trên cùng relay.

**Tùy chọn (khi có dịp gặp):** chạy lại trên LAN Ethernet với `iperf3` làm mốc để có số đo sạch,
không phụ thuộc Internet. Không bắt buộc.

---

# Phase 2: Tính năng bổ sung (tuần 2–3)

Bắt đầu ngay khi M2 chạy được; các việc độc lập nên làm song song với M3. Nếu chỉ còn 2 tuần
thì giữ 3 việc N (duyệt + chặn người lạ, kéo thả, tiến độ) và tăng cứng hub của D; vòng bù và
làn dữ liệu chỉ làm khi M3 cho thấy cần.

## Must

### Duyệt người vào + chặn người lạ (N)

**Việc:** `--approve` hỏi từng người bằng tên dễ đọc + IP; từ chối được thì đuổi thật khỏi hub;
sau khi đủ N người đã duyệt thì từ chối người tới sau; hết hạn phiên `--timeout`; người nhận
có `--name`.

**Cách làm:** dùng móc `AuthorizeReceiver` của T. Sau khi PAKE xong (người lạ không biết code
thì không tới được bước này), in `"binh-laptop" (203.0.113.5) muốn vào, nhận? [y/N]` bằng
`utils.GetInputContext`; tên đã được làm sạch (bỏ ký tự điều khiển, ≤ 32 ký tự). Trả lời `N`,
hoặc đã đủ N người: gửi `reject` qua `DIRECT` rồi gửi khung `KICK` (`contracts.md` mục 2.3).
Chống làm phiền theo `contracts.md` mục 3.5: hạn PAKE 10 giây, hàng đợi lời nhắc (cách nhau 3
giây), nhớ cặp (tên, IP) đã từ chối, tự từ chối không nhắc sau khi đủ N. Không dùng IP để chặn.
Không có khung `LOCK`: hub không biết ai được duyệt. `--timeout` là `context.WithTimeout`
bao quanh phiên. L thêm cờ `--name` phía nhận (mặc định hostname) và gửi trong `pake1`.

**Kiểm chứng:** hai người nhận vào, người gửi trả lời y/N: người bị từ chối không nhận byte
nào và kết nối bị hub đóng (`ss -tn` trên relay không còn kết nối đó); với `--receivers 3`,
máy thứ 4 có code bị từ chối và bị đuổi; một client viết tay cố giữ kết nối sau `reject` vẫn bị
đóng; một người bị từ chối vào lại 5 lần chỉ thấy tối đa một lời nhắc mỗi 3 giây, rồi tự bị từ chối; tên chứa mã escape (`\x1b[31m`) hiện ra đã làm sạch; để quá `--timeout` thì người gửi
thoát với thông báo rõ.

### Kéo thả đường dẫn trong terminal (N)

**Việc:** chuẩn hóa đường dẫn có dấu nháy, `\ `, `file://`, khoảng trắng cuối, nhiều file kéo
cùng lúc.

**Cách làm:** hàm `NormalizeDroppedPaths(arg string) []string` trong `src/multisend`: tách
bằng bộ phân tích có xét nháy đơn/kép và `\ `, bỏ `file://` rồi giải mã phần trăm
(`url.PathUnescape`), cắt khoảng trắng cuối, kiểm tra `os.Stat`. Gọi cho từng đối số dòng lệnh
và cho dòng người dùng dán vào khi không có đối số.

**Kiểm chứng:** kéo 3 file từ trình quản lý file vào terminal sau `croc send --receivers 3 `:
cả 3 được liệt kê và gửi, kể cả tên có dấu cách. Thử thêm trên Windows Terminal và một
terminal Linux.

### Tiến độ từng người trong terminal (N)

**Việc:** `An 80% | Bình xong | Chi đang chờ` và tóm tắt cuối.

**Cách làm:** người nhận gửi `progress` (tối đa 2 lần/giây) qua `DIRECT`; người gửi giữ bảng
`memberID → {tên, %, trạng thái}` và vẽ lại một dòng bằng `\r` (hoặc `termui`). Khi
`MEMBER_LEFT` hay `fail` thì đổi trạng thái thành "mất kết nối"/"lỗi".

**Kiểm chứng:** quan sát khi chạy 4 người nhận; tắt một người thì dòng đó hiện "mất kết nối";
dòng tóm tắt cuối liệt kê đủ từng người.

### Tăng cứng hub (D)

**Việc:** giới hạn kích thước khung, kiểm thử việc loại người nhận chậm, mức log, `--max-rooms`.

**Cách làm:** từ chối khung > 64 KB bằng `ERROR bad-frame` rồi đóng; cờ `--max-rooms` đếm
phòng `multi:` đang mở; log theo mức có sẵn trong `src/logger`; công cụ
`tools/multibench` có chế độ "người nhận không đọc" và "người nhận chậm" (đọc 1 KB/s).

**Kiểm chứng:** một người nhận không bao giờ đọc: bị loại trong 30 giây, các máy khác không
bị ảnh hưởng; gửi khung 1 MB thô vào cổng: relay đóng kết nối, không sập; mở quá `--max-rooms`
phòng: phòng mới bị từ chối.

### Làn dữ liệu (D + L) — chỉ khi M3 cho thấy một luồng chậm

**Việc:** thêm kết nối dữ liệu phụ (`multi:<room>#lane<j>`).

**Cách làm:** người gửi mở thêm k kết nối `JOIN` vào phòng `multi:<room>#lane<j>`; hub gắn
chúng vào cùng danh sách người nhận và phát `DATA` xen kẽ theo `offset`. Người nhận ghi theo
`offset` nên thứ tự đến không quan trọng. Chọn k mặc định theo kết quả benchmark.

**Kiểm chứng:** chạy lại benchmark với 1/2/4 làn; chọn mặc định; ghi số trước/sau vào
`docs/multi-e2e-test.md`; `sha256` vẫn khớp.

### Vòng bù / người vào muộn / thử lại (L)

**Việc:** sau lượt đầu, người nhận báo các đoạn còn thiếu; người gửi phát lại hợp của các đoạn
thiếu cho tới khi không ai thiếu; người nhận mất kết nối được nhận tiếp.

**Cách làm:** người nhận giữ bitmap theo khối 32 KB (hoặc danh sách đoạn như `ranges` của
croc) và gửi `done` kèm các đoạn thiếu; người gửi hợp (union) các đoạn thiếu của mọi người
rồi gửi lại chúng, lặp tới khi tất cả báo đầy đủ. Người nhận vào lại bằng cùng code gửi
`ready` kèm bitmap hiện có.

**Kiểm chứng:** tắt một người nhận ở 50% rồi chạy lại với cùng code: máy đó xong mà các máy
khác không bị gửi lại cả file; người gửi chỉ thoát khi cả N xong; số byte phát lại gần đúng
với phần thiếu.

## Should

- Gửi thư mục hoàn thiện (bố cục tương đối qua `receivefs`, thư mục rỗng) (N/L).
- `--throttle` ở người gửi (dùng bộ giới hạn có sẵn) cho demo (N).
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
| 1 (**MVP**) | M0, M1a, M2, M4 (Azure) | M0, M1b, ghép M2 | M0, M1c, ghép M2 | M0, M1d, ghép M2, khung `multi-e2e-test.md` |
| 2 | Tăng cứng hub | Review mật mã, tự nhận dạng code | Duyệt người vào, kéo thả, tiến độ | M3 (đo qua Azure), vòng bù |
| 3 | Làn dữ liệu (cùng L, nếu cần), sửa lỗi | Sửa lỗi, review cuối | Hoàn thiện, `--throttle` | Chạy lại ma trận đầu-cuối, biểu đồ benchmark |

**Cổng cuối tuần 1:** MVP chạy từ xa qua Azure (ma trận lượt 1–4 đạt). Chưa đạt thì tuần 2 chỉ
sửa MVP, không bắt đầu Phase 2.

Đóng băng tính năng 2 ngày trước hạn; hai ngày đó chỉ chạy lại ma trận đầu-cuối và sửa lỗi.

# Rủi ro theo người

- **D:** nhánh `multi:` trong `tcp.go` và loại người nhận chậm là phần dễ sai nhất; giữ nhánh
  nhỏ và test bằng đường truyền trong bộ nhớ trước khi chạm TCP thật. Azure phải xong sớm vì
  cả nhóm thử MVP trên đó.
- **T:** nonce và `dataKey` phải đúng ngay từ đầu; nhờ một người khác review `multisession`
  trước khi ghép M2.
- **N:** `cli.go` chỉ một dòng đăng ký; mọi logic CLI nằm trong `cli/multi.go` để tránh xung
  đột với L.
- **L:** số liệu benchmark phải đo trên mạng thật; chưa có số thì chưa viết số vào tài liệu.
