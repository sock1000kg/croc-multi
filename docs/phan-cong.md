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
| **D** | Hub (logic + gắn vào relay), Azure/Terraform/Ansible, người nhận giả, tăng cứng hub | `src/multihub/`, `src/tcp/multi_room.go` (+1 nhánh trong `tcp.go`), `deploy/`, `tools/faultinject/` |
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
M3 (đo tốc độ) đã đo ngày 21/11, xem mục M3.
```

### Trạng thái (cập nhật 21/11/2026)

| Mốc | Trạng thái | Ghi chú |
|---|---|---|
| M0 | ✅ | `croc send --help` có `--receivers/--approve/--timeout`, `croc --help` có `--multi` |
| M1a hub | ✅ | `src/multihub`; kiểm chứng bằng `hub_test.go` thay cho `tools/hubdemo` (không viết) |
| M1b trao khóa | ✅ | L viết `multiwire` + `multisession` trong PR #12; kiểm chứng bằng `session_test.go` thay cho `tools/sessiondemo` (không viết). T vẫn cần review mật mã |
| M1c gửi | ✅ | `multisend.Send`; chỉ file thường, chưa có thư mục/`--approve`/giới hạn tốc độ |
| M1d nhận | ✅ | `multirecv.Receive`; `TestFullM1InMemory` là bài thử "M1 xong" |
| M2 relay thật | ✅ | Thử tay một máy 18/11: 3 người nhận, `sha256` khớp, tải lên ≈ 1× file, 1-1 vẫn chạy. Relay cũ: báo lỗi ngay nhưng chưa đúng câu (xem "Tăng cứng hub") |
| M4 Azure | ✅ | Relay `138.91.38.64:9009` đang chạy (dựng lại 21/11 sau khi bản cũ `4.194.7.75` bị xóa) (triển khai lại sau mỗi lần sửa hub: `deploy/build.sh` + playbook) |
| M3 đo tốc độ | ✅ | Đo 21/11 qua Azure (`scripts/bench/azure.sh`): multi-send 1 người nhận ≈ croc 1-1 (chênh ~4%); không cần làn dữ liệu. Số nhiều người nhận lấy từ ma trận MVP |
| MVP từ xa | 🟡 | Lượt đầu 21/11: 1 gửi (1,48 GB) → 2 nhận; người gửi chết giữa chừng do lỗi `--timeout` (`plan.md` mục 11). Chạy lại ma trận với `--timeout 3h` |

## M0. Khóa hợp đồng (ngày 1, T dẫn dắt)

| Ai | Việc | Bài thử chạy tay |
|---|---|---|
| T | Rà soát và chốt `docs/contracts.md` (bản khởi đầu đã có; giải quyết các điểm ở mục 8 của nó); tạo `src/multiwire` (mã hóa/giải mã khung, cấu trúc thông điệp) | `go build ./...` thành công; chương trình nhỏ in ra một khung mẫu dạng hex và đọc lại đúng |
| D | Khung `src/multihub` + đường truyền trong bộ nhớ trong bộ nhớ; khung `deploy/` | `go run ./tools/faultinject --help` chạy được |
| N | Khung `src/multisend`, `src/cli/multi.go` (cờ `--receivers`, `--approve`, `--timeout`) | `croc send --help` hiện cờ mới |
| L | Khung `src/multirecv`, cờ `--multi` | `croc --help` hiện cờ mới |

**Xong khi:** cả 4 chạy được lệnh thử của mình, và cả nhóm đã đọc và đồng ý `contracts.md`.

## M1a. Hub (D)

**Việc:** `src/multihub`: vai trò SENDER/RECEIVER, `JOIN_ACK`, `DIRECT` (người nhận → người
gửi), `DATA` phát cho mọi người nhận, hàng đợi giới hạn mỗi người, loại người nhận đứng im
quá 30 giây, đuổi thành viên theo lệnh `KICK` của người gửi, khóa khi có `DATA` đầu tiên.

**Cách làm:**
- Logic hub không đụng tới mạng thật: chỉ làm việc với `multihub.Conn` (đọc/ghi khung).
  `multihub.Pipe()` dựng cặp `Conn` trong bộ nhớ từ `net.Pipe` bọc bằng `comm.Comm`.
- Mỗi phòng một goroutine đọc từ người gửi; mỗi người nhận một goroutine ghi, đọc từ hàng
  đợi có giới hạn (kênh `QueueBytes / 32 KB` khung, ≈ 8 MB). Hàng đợi đầy thì `DATA` của người
  gửi bị chặn (đó chính là backpressure; không cần cơ chế riêng).
- Loại người nhận chậm: khi hub đưa khung vào hàng đợi đã đầy, nó chờ tối đa `StallTimeout`
  (30 giây); hết hạn thì đóng kết nối người đó (lệnh `Send` đang kẹt trả lỗi, goroutine ghi
  thoát) và báo `MEMBER_LEFT` lý do 1 cho người gửi. Không dùng `SetWriteDeadline`: mỗi lần
  `comm.Comm` đọc nó gọi `SetDeadline(time.Time{})`, xóa luôn hạn ghi.
- Hub chỉ đọc phần đầu khung (`type/from/to`), không giải mã payload.

**Kiểm chứng** (thực tế dùng `src/multihub/hub_test.go`, không viết `tools/hubdemo`): `go run ./tools/hubdemo` (D viết) chạy 1 người gửi + 3 người nhận trong cùng
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

**Kiểm chứng** (thực tế dùng `src/multisession/session_test.go`, không viết `tools/sessiondemo`): `go run ./tools/sessiondemo` (T viết), 1 người gửi + 2 người nhận trên hub
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
1. T1: `croc relay`. T2: `croc --relay localhost:9009 send --receivers 3 big.bin` (in code).
   Không có `--relay` thì croc dùng relay công cộng, không có hub.
2. T3, T4, T5: `croc --relay localhost:9009 --multi CODE`, mỗi terminal một thư mục riêng.
   Cả ba nhận được file, `sha256` khớp.
3. Dùng relay cũ (relay công cộng): người gửi báo "relay này không hỗ trợ multi-send" sau vài
   giây, không treo.
4. `croc send file` / `croc CODE` kiểu 1-1 vẫn chạy bình thường trên cùng relay.

## M4. Relay trên Azure (D; khung làm từ ngày 1, chạy thật ngày 6)

**Việc:** `deploy/terraform` dựng 1 VM có IP công khai, mở TCP 9009–9013; `deploy/ansible` cài
`croc` và chạy `croc relay` như dịch vụ.

**Cách làm (đã làm như sau):**
- Terraform (`deploy/terraform`): resource group, VM `Standard_B1s`, IP tĩnh, NSG mở 22,
  9009–9013 và 5201 (cho `iperf3`), xuất `public_ip` và `ssh_user` (`croc`). Đăng nhập VM chỉ
  bằng khóa SSH (`admin_ssh_key_path` trong `terraform.tfvars`), không có mật khẩu.
- Build ở máy mình: `deploy/build.sh` tạo `dist/croc-linux-amd64` từ thư mục đang checkout (VM
  không cài Go, không clone repo).
- Ansible (`deploy/ansible/playbooks/relay.yaml`): cài `iperf3` và chạy nó thường trực trên
  cổng 5201 (`iperf3.service`; tắt bằng `iperf3_enabled: false` trong `vars.yaml`), tạo user `croc-relay`, chép
  binary vào `/usr/local/bin/croc`, tạo unit `systemd` chạy
  `croc relay --ports {{ relay_ports }} {{ relay_extra_args }}` với `CROC_PASS` lấy từ
  `relay_password`. Triển khai lại = `deploy/build.sh` rồi chạy lại playbook.
- Mật khẩu relay ở `deploy/ansible/vars.yaml`, IP ở `inventory.ini`; cả hai bị `.gitignore`.
- Cờ toàn cục như `--debug` phải đứng trước `relay`, nên **không** đặt được qua
  `relay_extra_args`; muốn log debug thì sửa dòng `ExecStart` trong
  `templates/croc-relay.service.j2` thành `croc --debug relay ...`. Xem log:
  `ssh croc@<ip> 'sudo journalctl -u croc-relay -f'`.

**Kiểm chứng:**
1. `terraform apply` rồi `ansible-playbook` chạy xong không lỗi.
2. `nc -vz <ip> 9009` từ máy bất kỳ báo mở.
3. Chạy lại bài thử M2 bằng `croc --relay <ip>:9009 ...` (cờ toàn cục đứng trước `send`, mật
   khẩu trong `CROC_PASS`) từ máy của D, rồi từ máy của một người khác ở mạng khác.

## M3. Đo tốc độ (✅ đo 21/11)

Qua mạng thật, tốc độ tuyệt đối bị giới hạn bởi chặng chậm nhất (upload nhà người gửi, download
người nhận chậm nhất, VM `B1s`), nên M3 trả lời câu hỏi **tương đối**: cùng máy, cùng relay,
multi-send 1 người nhận (1 kết nối) có chậm hơn croc 1-1 (4 kết nối) nhiều không.

**Kết quả (đo 21/11/2026, `scripts/bench/azure.sh`):** một laptop ở Việt Nam (WSL2) làm cả
người gửi lẫn người nhận, relay `Standard_B1s` ở Azure `southeastasia`, file ngẫu nhiên 300 MB,
mỗi trường hợp chạy 2 lần, `sha256` khớp ở mọi lần.

| Đo | Lần 1 | Lần 2 |
|---|---|---|
| `iperf3`, 1 luồng TCP, lên / xuống | 296 / 297 Mbit/s | |
| `iperf3`, 4 luồng song song, lên | 531 Mbit/s | |
| croc 1-1 qua relay (4 kết nối dữ liệu) | 208 Mbit/s (12,1 s) | 168 Mbit/s (15,0 s) |
| multi-send, 1 người nhận (1 kết nối) | 187 Mbit/s (13,5 s) | 176 Mbit/s (14,3 s) |
| multi-send, 3 người nhận trên cùng máy | 58 Mbit/s mỗi người (43 s) | 25 Mbit/s mỗi người (99 s) |
| Số byte người gửi tải lên (mọi N) | 314 947 292 (1,0012 × file) | |

Kết luận:
- **Multi-send 1 người nhận ≈ croc 1-1** (181 so với 188 Mbit/s trung bình, chênh ~4%, nhỏ
  hơn độ dao động giữa hai lần chạy). Đạt mục tiêu "chậm hơn không quá ~20%" ở `plan.md` mục 8.
- `iperf3` cho thấy một luồng TCP (~295 Mbit/s) chậm hơn 4 luồng (~530 Mbit/s), nhưng cả croc
  1-1 lẫn multi-send đều dừng ở ~180–200 Mbit/s, tức nghẽn nằm chỗ khác (CPU mã hóa, VM 1 vCPU),
  không phải ở số kết nối. **Không cần "làn dữ liệu"**.
- Số byte tải lên không tăng theo N: đúng mục tiêu chính.
- Số N=3 **không dùng để kết luận**: cả 3 người nhận chung đường xuống của một laptop và VM 1
  vCPU sao chép 3 lần; hai lần chênh nhau 2,3 lần (tín dụng CPU của VM còn 34, không bị bóp).
  Tốc độ với nhiều người nhận phải đo trong ma trận MVP với các máy thật khác nhau.
- Đo cục bộ (relay `localhost`, 500 MB, 16 CPU): 1-1 ~98 MB/s, multi 1 người ~93 MB/s, tức code
  đủ nhanh cho mọi đường truyền gia đình.

Chạy lại: `CROC_PASS=... scripts/bench/azure.sh ./croc <ip>:9009 300 1 3`. Trên nhiều máy
thật (ma trận MVP) không dùng script này: mỗi người chạy `iperf3 -c <ip> -t 5` và `-R` (VM luôn
chạy `iperf3` server, xem M4), rồi gửi như bình thường và chép dòng thời gian/tốc độ mà giao
diện terminal in ra (việc của T) vào `docs/multi-e2e-test.md`. Trước khi TUI xong, đo bằng tay
từ lúc người gửi bắt đầu gửi tới lúc từng người nhận báo xong (không tính phòng chờ).

**Việc:** `scripts/bench/run.sh` chạy file 1 GB tới 1, 2, 4, 8 người nhận; in thời gian và số
byte người gửi tải lên; chạy kèm một lần `croc` 1-1 cùng file, cùng relay.

**Cách làm:**
- Script tạo file bằng `head -c 1G /dev/urandom`, khởi động `croc relay` trên cổng riêng,
  chạy người gửi và k người nhận nền với `--out` khác nhau, đo bằng `date +%s.%N`.
- Đọc số byte người gửi từ dòng tổng kết của `multisend` (đã in ở M1c); với croc 1-1 dùng
  thời gian tường.
- Chạy hai kiểu: một máy (relay `localhost`, đo giới hạn CPU/mã hóa) và qua relay Azure (đo
  băng thông thật). Chỉ kiểu thứ hai mới đưa vào kết luận.

**Kiểm chứng ban đầu:** chạy script, dán bảng kết quả vào `docs/multi-e2e-test.md`. Đối chiếu
với mục 8 của `plan.md`. Nếu 1 người nhận chậm hơn mục tiêu, mở việc "làn dữ liệu" (Phase 2
Must). Thực tế: kết quả nằm ở trên; `docs/multi-e2e-test.md` vẫn chưa có (L tạo khi chạy ma
trận MVP, chép bảng này vào).

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
export CROC_PASS=<mật khẩu relay>       # mọi người, một lần mỗi terminal
croc --relay <ip>:9009 send --receivers 3 --timeout 3h big.bin
croc --relay <ip>:9009 --out ~/nhan --multi CODE
```

Mọi người build từ cùng `main` (`git log --oneline -1` giống nhau). `--timeout 3h` là cách
tránh lỗi `--timeout` cho tới khi D sửa. `iperf3 -c <ip> -t 5` (tải lên) và `-R` (tải xuống)
cần `iperf3` cài ở máy mình (`sudo apt install iperf3`); VM luôn chạy `iperf3` server.

**Tiêu chí đạt cho mỗi lượt:**
- `sha256sum` giống nhau trên mọi máy.
- Người gửi chỉ thoát sau khi mọi người nhận xong.
- Ghi thời gian và băng thông upload của người gửi (đo bằng `speedtest` hoặc `iperf3` tới
  relay trước); số byte người gửi tải lên ≈ kích thước file, không nhân với số người nhận.

**Thử lỗi (dùng `Ctrl+C`, tắt Wi‑Fi, hoặc kill tiến trình thay cho việc rút dây):**
- Tắt một máy nhận ở khoảng 50% (MVP: máy đó lỗi gọn và người gửi báo; nếu làm vòng bù (Could) thì
  máy đó nhận tiếp).
- Máy thứ tư biết code nhưng vào sau khi đủ người thì bị từ chối.
- `croc` 1-1 vẫn chạy trên cùng relay.

**Tùy chọn (khi có dịp gặp):** chạy lại trên LAN Ethernet với `iperf3` làm mốc để có số đo sạch,
không phụ thuộc Internet. Không bắt buộc.

---

# Phase 2: Tính năng bổ sung (tuần 2–3)

Bắt đầu ngay khi M2 chạy được; các việc độc lập nên làm song song. Nếu chỉ còn 2 tuần
thì giữ 2 việc N (duyệt + chặn người lạ, kéo thả), giao diện terminal (T phía gửi, L phía nhận), tăng cứng hub và
sửa `--timeout` của D; vòng bù là Could. Làn dữ liệu bỏ: M3 cho thấy một kết nối đã
ngang croc 1-1.

## Must

Mỗi việc ghi đường dẫn và tên hàm trong code hiện tại (21/11). Việc chạm vùng của người khác
thì báo chủ vùng trước. Đổi chữ ký hàm công khai thì sửa `contracts.md` mục 5 cùng PR.

### Duyệt người vào + chặn người lạ (N, L làm `--name`)

**Việc:** `--approve` hỏi từng người bằng tên + IP sau khi PAKE xong; từ chối thì `reject` +
`KICK`; nhớ người đã từ chối; người nhận có `--name`.

**Đã có:** cờ `--approve` (nhưng `multisend.Send` đang trả lỗi "not yet supported"); móc
`approve func(ReceiverInfo) bool` trong `multisession.AuthorizeReceiver`, gọi **sau** khi PAKE
xác nhận, từ chối thì nó tự gửi `reject` "refused"; `sendSession` đã `KICK` mọi người có
`AuthorizeReceiver` trả lỗi; hub đã giới hạn N người cùng lúc (`room-full`).

**Cách làm:**
1. `src/multisend/client.go`, `Send`: bỏ dòng trả lỗi khi `opts.Approve`.
2. `sendSession`, nhánh `TypeMemberJoined`: đọc IP từ payload
   (`memberID u16 | addrLen u8 | addr`, tức `addr = f.Payload[3:3+f.Payload[2]]`), lưu vào
   `addrs[id]`. Map này chỉ goroutine chính ghi; goroutine từng người cần đọc thì truyền `addr`
   vào closure lúc tạo goroutine.
3. Thay `nil` bằng một hàm `approve` khi `opts.Approve` bật. Hàm này được gọi **song song** từ
   goroutine của từng người, nên phải xếp hàng: một `sync.Mutex` bao quanh cả câu hỏi, và giữ
   `lastPrompt` để chờ đủ 3 giây giữa hai lời nhắc. Hỏi bằng
   `utils.GetInputContext(ctx, prompt)` với `ctx` **của phiên** (không phải `authCtx` 10 giây,
   vì người dùng có thể trả lời chậm hơn 10 giây).
4. Làm sạch tên trước khi in, trong hàm mới `sanitizeName` ở `multisession`: bỏ chuỗi escape
   (`termui.Plain` chỉ bỏ mã màu `\x1b[...m`, nên tự lọc thêm), bỏ mọi rune `< 0x20` và `0x7f`,
   cắt còn 32 rune; tên rỗng thì dùng `"?"`.
5. Nhớ người bị từ chối: `refused map[string]bool` với khóa `tên + "|" + IP`, kiểm tra trước khi
   hỏi; có thì trả `false` ngay, không hỏi.
6. Đủ N người đã duyệt thì tự trả `false`. Hub đã chặn người thứ N+1 khi N người còn trong
   phòng, nên trường hợp này chỉ xảy ra khi có người đã duyệt rồi rời đi.
7. `--name` (L): thêm `&cli.StringFlag{Name: "name"}` trong `registerMultiRecv`
   (`src/cli/multi.go`, cạnh cờ `--multi`), thêm `Name` vào `multirecv.ReceiveOptions`, và thêm
   tham số `name` cho `multisession.JoinSession` (hiện dùng `os.Hostname()` trong
   `session.go`); trống thì vẫn dùng hostname. Sửa 2 chỗ gọi trong test.

**Kiểm chứng:** hai người nhận vào, người gửi trả lời y/N: người bị từ chối không nhận byte
nào và kết nối bị hub đóng (`ss -tn` trên relay không còn kết nối đó); một client viết tay cố giữ
kết nối sau `reject` vẫn bị đóng; một người bị từ chối vào lại 5 lần chỉ thấy tối đa một lời
nhắc mỗi 3 giây, các lần sau tự bị từ chối; tên chứa mã escape (`\x1b[31m`) hiện ra đã làm sạch.

### Kéo thả đường dẫn trong terminal (N)

**Việc:** chuẩn hóa đường dẫn có dấu nháy, `\ `, `file://`, khoảng trắng cuối, nhiều file kéo
cùng lúc.

**Cách làm:**
1. Hàm `NormalizeDroppedPaths(arg string) ([]string, error)` trong file mới
   `src/multisend/paths.go`: duyệt từng ký tự, tách theo khoảng trắng nằm ngoài nháy đơn/kép;
   `\ ` là dấu cách thoát **chỉ khi `runtime.GOOS != "windows"`** (trên Windows `\` là dấu phân
   cách thư mục); bỏ tiền tố `file://` rồi `url.PathUnescape`; cắt khoảng trắng cuối; mỗi kết
   quả phải `os.Stat` được.
2. `src/cli/multi.go`, `sendMulti`: chạy hàm này cho từng phần tử của `c.Args().Slice()` rồi
   nối lại trước khi gọi `multisend.Send`. Không có đối số thì in "Kéo thả file vào đây rồi nhấn
   Enter:" và đọc một dòng bằng `utils.GetInputContext`.
3. Test dạng bảng: `'a b.txt'`, `"a b.txt"`, `a\ b.txt`, `file:///tmp/a%20b.txt`, nhiều đường
   dẫn trên một dòng, đường dẫn Windows `"C:\Users\a b\x.zip"`.

**Kiểm chứng:** kéo 3 file từ trình quản lý file vào terminal sau `croc send --receivers 3 `:
cả 3 được liệt kê và gửi, kể cả tên có dấu cách. Thử thêm trên Windows Terminal và một
terminal Linux.

### Giao diện terminal: tiến độ, tốc độ, thông báo vào/ra (T: phía gửi, L: phía nhận)

Phát hiện khi chạy thử qua Azure (`plan.md` mục 11). **T làm phía người gửi** (vùng của N:
`src/multisend`, phần gửi của `src/cli/multi.go`; báo N trước khi sửa). **L làm phía người nhận**
(vùng của chính L: `src/multirecv`, phần nhận của `src/cli/multi.go`). Hai bên dùng chung một
định dạng in thời gian/tốc độ (Mbit/s, `1m02s`) để số của người gửi và người nhận so được với
nhau; thống nhất trước khi viết.

**Mục tiêu:**

```
Người gửi:
  Multi-send code: lion-quiet-tulip
  [2] binh-laptop (203.0.113.5) đã vào
  [2] binh-laptop sẵn sàng (1/2)
  [3] an-pc (198.51.100.7) đã vào
  [3] an-pc sẵn sàng (2/2), bắt đầu gửi
  [2] binh-laptop 64% 11.8 MB/s | [3] an-pc 61% 11.2 MB/s | tải lên 11.9 MB/s
  [3] an-pc xong: 1.38 GiB trong 58s (204 Mbit/s)
  Tóm tắt: 2/2 xong. Tải lên 1.38 GiB trong 1m02s (190 Mbit/s); chậm nhất [2] 1m02s
Người nhận:
  Đang chờ người gửi... / Đã vào phòng, chờ người gửi bắt đầu
  wider-face-yolo.zip  64% |██████▍   | (905 MB/1.4 GB, 11 MB/s) [1m20s:45s]
  Đã nhận 1.38 GiB trong 1m02s (190 Mbit/s)
  Đã lưu: /home/binh/nhan/wider-face-yolo.zip
```

**Đã có:** người nhận gửi `progress` (tối đa 2 lần/giây, có `bytes`) qua `DIRECT`;
`sendSession` gọi `opts.Progress` với `PeerProgress{ID, Bytes, State}` cho `ready`, `receiving`,
`done`, `failed`; `PeerProgress.Name` đã có nhưng chưa được điền; `termui.NewProgress` là thanh
tiến độ của croc 1-1 (có %, tốc độ, thời gian còn lại). **Chưa có:** `src/cli/multi.go` không
gắn `Progress` nên người gửi không in gì; chưa có trạng thái `joined`/`left`.

**Cách làm, người gửi (T)** (`src/multisend/client.go` + file mới `src/cli/multi_ui.go`):
1. Trong `sendSession`, gọi `report` thêm ở `TypeMemberJoined`
   (`State: "joined"`, `Name: <IP>`) và `TypeMemberLeft` (`State: "left"`).
2. Lấy tên người nhận mà không đổi chữ ký hàm: luôn truyền một hàm `approve` vào
   `AuthorizeReceiver` (khi không bật `--approve` thì nó chỉ ghi `info.Name` rồi trả `true`), và
   báo `ready` kèm tên đó. Phối hợp với N, vì N cũng sửa hàm `approve` này.
3. `multi_ui.go`: một kiểu `sendUI` giữ `map[uint16]*peerView{name, addr, bytes, state,
   speed}`, với `mu sync.Mutex` vì `Progress` được gọi từ nhiều goroutine (`report` đã khóa,
   nhưng giữ lock riêng cho an toàn). Sự kiện `joined/ready/left/failed/done`: xóa dòng tiến độ
   (`\r\x1b[K`), in một dòng sự kiện, rồi vẽ lại dòng tiến độ. Sự kiện `receiving`: chỉ cập nhật
   số liệu. Một goroutine `time.Ticker` 500 ms vẽ lại dòng tiến độ bằng `\r`.
4. Tốc độ: lưu `(bytes, time)` lần trước; `v = Δbytes/Δt`; làm mượt
   `speed = 0.3*v + 0.7*speed`. % = `bytes / session.TotalSize`, nên truyền `TotalSize` cho UI
   (thêm trường `Total` vào `PeerProgress` hoặc một callback khi bắt đầu). Tốc độ tải lên tổng ≈
   tốc độ của người nhanh nhất (hub không cho người gửi vượt quá hàng đợi 8 MB).
5. `sendMulti` tạo `sendUI` rồi truyền `SendOptions{..., Progress: ui.update}`. Sau khi
   `multisend.Send` trả về thì dừng ticker và in tóm tắt từ `map`.
6. Không phải terminal (`_, isTTY := termui.Output(os.Stderr)` trả `false`): không dùng `\r`,
   chỉ in dòng sự kiện và mỗi 5 giây một dòng tiến độ.
7. **Đo thời gian (thay cho benchmark khi chạy trên nhiều máy thật).** Đồng hồ bắt đầu ngay
   trước `StreamFiles`, **không** tính thời gian ở phòng chờ (người vào chậm không làm sai số).
   Báo cho UI qua `PeerProgress{State: "sending"}` lúc bắt đầu. Khi một người `done`, in thời
   gian từ lúc bắt đầu tới `done` của người đó và tốc độ `TotalSize*8/giây` (Mbit/s). Tóm tắt
   cuối in tổng byte tải lên (giá trị `sent` của `StreamFiles`), thời gian tải lên, tốc độ và
   người chậm nhất. Dùng đơn vị Mbit/s để so được với `iperf3`.

**Cách làm, người nhận (L)** (`src/cli/multi.go` + `src/multirecv/client.go`):
1. Thêm `Status func(msg string)` vào `multirecv.ReceiveOptions` (sửa `contracts.md` mục 5.4).
   `Receive` gọi nó khi chờ `no-sender` ("Đang chờ người gửi..."), sau `JoinSession` ("Đã vào
   phòng, chờ người gửi bắt đầu") và khi xong ("Đã lưu: <đường dẫn tuyệt đối>" cho từng file,
   dùng `filepath.Abs(filepath.Join(OutDir, f.Path))`).
2. Trong `receiveMulti`, thay hàm `Progress` đang in `X / Y bytes` bằng thanh
   `termui.NewProgress(termui.ProgressConfig{Max: total, Description: tên file, Writer: w,
   ColorEnabled: color})` với `w, color := termui.Output(os.Stderr)`. Tổng chỉ biết sau
   `JoinSession`, nên tạo thanh ở lần gọi `Progress` đầu tiên rồi `bar.Set64(bytes)`;
   `bar.Finish()` khi xong.
3. **Đo thời gian:** đồng hồ bắt đầu ở khung `DATA` đầu tiên trong `ReceiveStream`, không phải
   lúc vào phòng (vì người nhận còn chờ những người khác sẵn sàng). Khi xong in
   "Đã nhận X trong Ys (Z Mbit/s)" qua `Status`.

**Kiểm chứng:** chạy 4 người nhận qua relay Azure: người gửi thấy từng người vào và sẵn sàng;
tiến độ và tốc độ của từng người cập nhật; tắt một người thì dòng đó hiện "mất kết nối"; tóm tắt
cuối liệt kê đủ từng người; người nhận có thanh tiến độ và in đường dẫn file đã lưu;
`croc ... 2> log.txt` cho ra file đọc được (không có `\r`). Đối chiếu đo thời gian: chạy
`scripts/bench/azure.sh` và một lần gửi tay với cùng file trên cùng máy; tốc độ TUI in ra lệch
không quá ~10% so với script.

### Tăng cứng hub (D)

**Đã xong:** `tools/faultinject` với `--mode stall|slow|silent`; cờ relay
`--multi-max-receivers`, `--multi-queue-mb`, `--multi-stall-timeout`, `--multi-join-rate`; loại
người nhận đứng im 30 giây có test.

**Còn lại và cách làm:**
1. **Khung quá 64 KB:** `comm` nhận được khung tới cỡ MB, còn `multiwire.Decode` mới là chỗ từ
   chối khung > 64 KB. Trong `src/multihub/hub.go`:
   - `serveSender`: khi `multiwire.Decode` lỗi thì gửi `errorFrame(ErrBadFrame, "frame too
     large")` rồi mới `return` (hiện tại đóng luôn, không báo).
   - `serveReceiver`: hiện gửi `bad-frame` rồi `continue`, tức giữ kết nối. Đổi thành: xếp lỗi
     vào hàng đợi, `dropMember(..., LeftNetwork, true)`, rồi `return`.
2. **Giới hạn số phòng multi:** `--max-rooms-open` của relay không đếm phòng `multi:`, vì nhánh
   `multi:` trong `clientCommunication` (`src/tcp/tcp.go`) chạy trước `admitToRoom`.
   - Thêm `MaxRooms int` vào `multihub.Config`. Trong `serveSender`, dưới `hub.mu`, nếu
     `len(hub.rooms) >= MaxRooms` thì từ chối bằng mã lỗi mới `ErrTooManyRooms = 8` (thêm vào
     `multiwire/wire.go` và `errorNames` trong `errors.go`).
   - Cờ `--multi-max-rooms`: làm giống `--multi-max-receivers` trong `src/cli/cli.go` (khai báo
     cờ cạnh nó, đọc và kiểm tra quanh dòng 1203, truyền vào `multihub.New` ở dòng 1221).
3. **Log:** `src/multihub` chưa log gì. Dùng `log "github.com/schollz/croc/v11/src/logger"`
   như `src/tcp/tcp.go`:
   - `Infof` khi tạo/xóa phòng (cắt tên phòng còn 8 ký tự hex, không log mã);
   - `Debugf` cho người vào/ra kèm lý do (`LeftQuit/LeftSlow/LeftNetwork`), `KICK` và lỗi
     khung;
   - không log payload.
4. **Báo đúng khi gặp relay cũ:** trong `multisession.Dial` (vùng của T, báo T), coi lỗi giải
   mã khung hoặc "invalid JOIN_ACK" từ `NewLink` cũng là "relay này không hỗ trợ multi-send",
   giống nhánh hết 5 giây.

**Kiểm chứng:**
- Một người nhận không bao giờ đọc: bị loại trong 30 giây, các máy khác không bị ảnh hưởng.
- Gửi khung 1 MB thô vào cổng: relay báo `bad-frame`, đóng kết nối, không sập.
- Mở quá `--multi-max-rooms` phòng: phòng mới bị từ chối với thông báo rõ.
- `croc --debug relay`: thấy log vào/ra.
- `--relay` trỏ vào relay croc cũ: thông báo "không hỗ trợ multi-send".

### Sửa `--timeout` của người gửi (D)

Lỗi phát hiện khi chạy thử qua Azure (`plan.md` mục 11): `--timeout` (mặc định 10 phút) cắt cả
phiên đang tải lên, và người gửi chỉ báo `use of closed network connection`.

**Việc:** `--timeout` chỉ giới hạn thời gian chờ ở phòng chờ (đến khi đủ N người sẵn sàng),
đúng như mô tả của cờ; hết hạn thì báo rõ "hết thời gian chờ người nhận (k/N đã sẵn sàng)".

**Cách làm** (`src/multisend/client.go`, vùng của N, báo N):
1. `Send`: bỏ khối `if opts.Timeout > 0 { ctx, cancel = context.WithTimeout(...) }`.
2. `sendSession`, ngay trước vòng `for len(ready) < opts.Receivers`: tạo
   `lobby := ctx` và nếu `opts.Timeout > 0` thì
   `lobby, cancelLobby = context.WithTimeout(ctx, opts.Timeout)` (nhớ `defer cancelLobby()`).
3. Trong `select` của vòng phòng chờ, đổi `case <-ctx.Done(): return ctx.Err()` thành
   `case <-lobby.Done():`. Nếu `ctx.Err() != nil` (Ctrl+C) thì trả `ctx.Err()`; ngược lại trả
   `fmt.Errorf("hết thời gian chờ người nhận: %d/%d đã sẵn sàng", len(ready), opts.Receivers)`.
4. Các goroutine PAKE vẫn dùng `ctx` của phiên (không phải `lobby`), để không bị cắt ngang ngay
   lúc vòng lặp vừa đủ người.
5. Pha tải lên và chờ `done` không có hạn tổng: người nhận chết đã có hạn 30 giây của hub và
   `MEMBER_LEFT`. Cập nhật `Usage` của cờ thành "max time to wait for receivers to be ready
   (0 = no limit)".

**Kiểm chứng:**
- `--timeout 30s` và không ai vào: thoát sau 30 giây với thông báo hết giờ, mã 1.
- `--timeout 1m` với file đủ lớn để tải lên mất vài phút: truyền xong, `sha256` khớp.
- Test đơn vị trong `m1_integration_test.go`: phòng chờ trống với `Timeout: 200ms` trả lỗi hết
  giờ.

### Làn dữ liệu (D + L) — KHÔNG LÀM: M3 (21/11) cho thấy 1 kết nối ≈ croc 1-1 (giữ mô tả để tham khảo)

**Việc:** thêm k kết nối dữ liệu phụ từ người gửi để vượt giới hạn của một luồng TCP.

**Điều kiện trước:** người nhận hiện **chỉ chấp nhận khối theo thứ tự**
(`multirecv.Receiver.writeChunk` báo lỗi khi `offset > written`). Nhiều làn sẽ làm khối đến lệch
thứ tự, nên phải làm phần "nhận lệch thứ tự" trong "Vòng bù" (Could) trước (L).

**Cách làm:**
1. Hub (D): cho phép người gửi mở thêm kết nối `JOIN` với vai trò mới `RoleSenderLane = 3`
   (thêm vào `multiwire`) vào cùng phòng. Hub chỉ nhận `DATA` từ làn và phát giống `DATA` của
   kết nối chính. Làn không được gửi `DIRECT`/`KICK`.
2. Người gửi (N/D): `StreamFiles` phát khối lần lượt vòng tròn qua k `Link`. Mỗi làn một
   goroutine ghi, để một làn chậm không chặn các làn khác.
3. Người nhận (L): nhờ phần nhận lệch thứ tự, chỉ ghi theo `offset`; `file-end` đến khi mọi
   khối của file đã đủ.

**Kiểm chứng:** chạy lại benchmark với 1/2/4 làn; chọn mặc định; ghi số trước/sau vào
`docs/multi-e2e-test.md`; `sha256` vẫn khớp.

## Should

- **Gửi thư mục (N/L).** `multisend.Send` hiện từ chối mọi thứ không phải file thường.
  - Dùng `croc.GetFilesInfoContext(ctx, files, false, false, nil)`: nó trả `FileInfo` (dùng
    `FolderSource`, `FolderRemote`, `Name`) và danh sách thư mục rỗng.
  - Điền `FileEntry.Path` = đường dẫn tương đối dấu `/`, và `Session.EmptyDirs`.
  - `StreamFiles` nhận đường dẫn đầy đủ theo đúng thứ tự `Idx`.
  - Phía nhận đã hỗ trợ đường dẫn con và `EmptyDirs` qua `receivefs`.
- **Giới hạn tốc độ người gửi (N).** Dùng lại cờ toàn cục `--throttleUpload` của croc (ví dụ
  `500k`).
  - Tách phần phân tích ở `src/croc/croc.go` (khoảng dòng 460–490) thành một hàm dùng chung,
    điền `SendOptions.ThrottleBps`, và bỏ dòng trả lỗi trong `Send`.
  - Trong hàm `emit` gọi `limiter.WaitN(ctx, len(chunk))` với
    `rate.NewLimiter(rate.Limit(bps), 64*1024)`.
- **Nhấn Enter để bắt đầu sớm (N).** Trong vòng phòng chờ của `sendSession`, đọc stdin ở một
  goroutine. Enter khi đã có ít nhất 1 người `ready` thì thoát vòng; người chưa `ready` bị
  `KICK`.
- **Trang web cùng luồng**, tái sử dụng `web/` (chỉ khi còn dư hơn 3 ngày).

## Could

- Xoay `dataKey` theo file; người nhận gửi tiếp cho nhau; đường trực tiếp qua Tailcat.

### Vòng bù / người vào muộn / thử lại (Could: chỉ khi còn thời gian; L, hub: D)

Dời từ Must xuống Could ngày 22/11 để tập trung vào việc khác trước. Trước khi làm, phải chốt
thêm các điểm thiết kế còn mở ở cuối mục này.

**Việc:** người nhận rớt mạng vào lại bằng cùng code và chỉ nhận phần còn thiếu; người gửi chỉ
thoát khi cả N xong.

**Hiện tại chặn ở 3 chỗ:**
1. Hub khóa phòng ở `DATA` đầu tiên: người vào lại nhận `room-locked`.
2. `multirecv.Receiver.Close` xóa file tạm khi lỗi, nên không còn gì để nối tiếp.
3. Người nhận chỉ ghi theo thứ tự.

**Cách làm:**
1. **Nhận lệch thứ tự (L):** thay `fileState.written` bằng bitmap khối, dùng lại
   `bundle.NewChunks(size)` / `(*Chunks).Accept(offset, len)` trong `src/bundle` (đã có, khối
   32 KB). Khối trùng thì so sánh như hiện nay; `file-end` hợp lệ khi bitmap đủ.
2. **Giữ file dở (L):** khi lỗi mạng (không phải sai băm), `Close` giữ file tạm và ghi bitmap ra
   `.croc-multi-<băm tên phòng>.state` cạnh nó; lần vào lại cùng code thì đọc lại.
3. **Hub (D):** khi phòng đã khóa, vẫn cho người nhận vào nếu số người hiện có `< maxReceivers`
   (người đó vẫn phải qua PAKE và `--approve` của người gửi). Thay `room-locked` bằng điều kiện
   này.
4. **Thông điệp mới (T sửa `contracts.md` mục 3.4 và 4):**
   - người nhận gửi `{"t":"missing","files":[{"idx":0,"ranges":[[off,len],...]}]}` thay cho
     `ready` khi vào lại, hoặc kèm `done` khi thiếu;
   - người gửi gom (union) các đoạn thiếu, đọc lại bằng `os.File.ReadAt` và phát `chunk` +
     `file-end` cho các file đó, lặp tới khi mọi người `done`. Người khác nhận khối trùng thì bỏ
     qua (bước 1 đã cho phép).

**Kiểm chứng:** tắt một người nhận ở 50% rồi chạy lại với cùng code: máy đó xong mà các máy
khác không bị gửi lại cả file; người gửi chỉ thoát khi cả N xong; số byte phát lại gần đúng với
phần thiếu.

**Điểm thiết kế còn mở (ghi lại 22/11, chốt trước khi làm):**
- **Phân biệt người quay lại với người mới:** hub không biết ai là ai (mỗi lần vào là một mã
  thành viên mới). Đề xuất: người gửi đặt một `resumeToken` ngẫu nhiên trong `session` (đã mã
  hóa) của từng người; người nhận lưu nó trong file `.state` và đưa ra sau PAKE khi vào lại.
- **Người chưa từng ở trong phòng** mà vào khi có chỗ trống: cho vào (người vào muộn, phát lại cả
  file) hay từ chối? Đề xuất: từ chối, trừ khi người gửi bật cờ cho phép; nếu không thì "chờ đủ
  N người xong" không còn rõ là chờ ai.
- **`--approve`:** người có `resumeToken` hợp lệ được tự duyệt, không hỏi lại. Hỏi người mới
  giữa lúc đang truyền thì phải tạm dừng dòng tiến độ của giao diện terminal.
- **`DATA` trước khi xong PAKE:** sau khi hub cho vào lại, người mới nhận `DATA` ngay. Bộ đệm chỉ
  chứa khoảng nửa giây ở tốc độ đã đo; PAKE nhanh thì ổn (cùng `dataKey`, giải mã được sau),
  nhưng chờ người bấm duyệt thì hàng đợi đầy và **mọi người** dừng tới 30 giây. Cần hub chỉ phát
  `DATA` cho người đó sau khi người gửi gửi khung mới `ACTIVATE <memberID>`.


## Không làm (quyết định 22/11)

- Tự nhận dạng code multi trong `croc CODE`: người nhận luôn gõ `--multi`.
- `--password` trộn vào bí mật PAKE: `--code` đã đủ (`plan.md` mục 1).

---

# Lịch gợi ý

| Tuần | D | T | N | L |
|---|---|---|---|---|
| 1 (**MVP**) | M0, M1a, M2, M4 (Azure) | M0, M1b, ghép M2 | M0, M1c, ghép M2 | M0, M1d, ghép M2, khung `multi-e2e-test.md` |
| 2 | Sửa `--timeout`, tăng cứng hub | Review mật mã, giao diện terminal phía gửi | Duyệt người vào, kéo thả | `--name`, giao diện terminal phía nhận, `docs/multi-e2e-test.md` |
| 3 | Sửa lỗi; vòng bù phía hub nếu còn thời gian (Could) | Sửa lỗi, review cuối | Gửi thư mục, `--throttleUpload`, Enter để bắt đầu | Vòng bù nếu còn thời gian (Could), chạy lại ma trận đầu-cuối, biểu đồ benchmark (`scripts/bench/azure.sh`) |

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
