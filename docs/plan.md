# Kế hoạch: croc multi-send (gửi 1 người → N người qua hub relay)

Tài liệu này mô tả phạm vi, kiến trúc và cách triển khai trong **3 tuần**. Việc chia
cho từng người và các mốc kiểm tra nằm ở [`phan-cong.md`](phan-cong.md).

> Hợp đồng chi tiết (định dạng gói tin, giao diện Go, dòng lệnh) nằm ở
> [`contracts.md`](contracts.md); mục 5 dưới đây chỉ là bản phác thảo. T hoàn thiện
> `contracts.md` ở mốc M0.

## 1. Mục tiêu

Hôm nay croc chỉ gửi **một người → một người**. Muốn gửi cho 5 người phải mở 5 terminal
và tải lên 5 lần. Mục tiêu:

- **Người gửi tải lên một lần**, một hub (mở rộng từ relay có sẵn của croc) sao chép dữ
  liệu cho N người nhận.
- **Kết thúc khi người nhận cuối cùng nhận xong.**
- Tốc độ vẫn ở mức tốt (đo và so với croc 1-1 trên cùng đường truyền, xem mục 8).
- Mã hóa đầu-cuối như croc: relay không đọc được nội dung.
- Cải tiến sau MVP: người gửi duyệt từng người vào (tên dễ đọc + IP, đuổi thật bằng `KICK`), từ chối người tới sau khi đủ N, kéo thả
  đường dẫn trong terminal, thử lại/nhận bù.

Không làm: bảng vẽ, âm thanh/màn hình trực tiếp, swarm (người nhận gửi tiếp cho nhau),
dashboard, chế độ `--mode direct` chỉ để so sánh. Lý do bỏ mật khẩu riêng: croc đã có
`--code CỤM_TỪ_CỦA_BẠN` (ít nhất 6 ký tự), nên người gửi tự chọn bí mật và gửi qua kênh
khác nếu muốn; mật khẩu thứ hai không thêm bảo mật nếu gửi chung một kênh. Việc **duyệt
từng người vào** và **đuổi người bị từ chối** mới là thứ chặn người lạ có mã.

## 2. Những gì đã xác nhận khi đọc code

1. **`croc.Client` chỉ làm việc với một người** (`src/croc/croc.go`, khoảng 4000 dòng): một
   kết nối điều khiển (`c.connection(0)`), một khóa cặp (`c.Key`, `c.dataAEAD`), một
   hàng đợi khối (`senderChunkQueue`), cộng thêm Tailcat/DERP, phát hiện relay cục bộ và
   tự nối lại. Biến nó thành 1-N nghĩa là viết lại. **Quyết định: không sửa `Client`.** Làm
   một engine nhỏ mới (`src/multi*`) và dùng lại các mảnh có sẵn. `croc send` / `croc CODE`
   1-1 giữ nguyên.
2. **Đường dữ liệu hiện tại:** người gửi đọc khối (`TCP_BUFFER_SIZE/2` = 32 KB, hàm
   `sendData`), mã hóa bằng **khóa cặp**, gửi qua nhiều cổng dữ liệu của relay. Mỗi người nhận
   có khóa cặp khác nhau nên relay không thể sao chép một luồng cho nhiều người. **Cách sửa:
   người gửi mã hóa một lần bằng `dataKey` ngẫu nhiên của phiên**, và gửi `dataKey` cho từng
   người nhận qua kênh điều khiển đã xác thực bằng PAKE.
3. **Relay hiện tại** (`src/tcp/tcp.go`): `clientHandshake` (dòng 433) không phụ thuộc số
   người trong phòng (PAKE với relay bằng khóa cố định, mật khẩu relay, rồi tên phòng). Chỉ
   `clientCommunication` → `admitToRoom` (dòng 116, `roomInfo{first, second}`) và
   `pipeWithProtocol` bị đóng cứng cho 2 người. Tên phòng là một chuỗi tùy ý sau khi giải
   mã (`result.room`), nên tiền tố `multi:` chọn được kiểu phòng mới mà **không phải sửa bắt
   tay và không ảnh hưởng client cũ**.
4. **Tên phòng và bí mật PAKE** lấy từ `codephrase.Parse`
   (`src/codephrase/codephrase.go`): `RoomName` là sha256-hex của các từ chọn phòng,
   `PAKEPassphrase` là các từ còn lại. Relay không thấy bí mật này.
5. **Dùng lại nguyên trạng:** `tcp.ConnectToTCPServerControlContext` (bắt tay client–relay),
   `comm` (đóng gói gói tin), `codephrase`, `pakekey.Init/Derive/Confirm`, `crypt` (AEAD),
   `croc.GetFilesInfoContext` (liệt kê file/thư mục), `receivefs` (ghi file an toàn trong thư
   mục đích), `utils.HashFile`, bộ giới hạn tải lên của `--throttleUpload`
   (`golang.org/x/time/rate`), `termui`, `utils.GetInputContext` (câu hỏi duyệt), và cách
   đăng ký cờ/lệnh trong `src/cli/cli.go`.
6. **Relay cũ/công cộng** coi `multi:` là phòng 2 người bình thường nên người gửi sẽ chờ mãi.
   Vì vậy client bắt buộc chờ `JOIN_ACK` từ hub trong 5 giây (`multisession.Dial`); nếu không có
   thì báo "relay này không hỗ trợ multi-send, hãy dùng `croc relay` của bạn". **Thực tế (đã
   thử):** relay croc cũ trả lời ngay một gói không phải khung hub, nên client báo
   `invalid multi frame size` thay cho câu trên (không treo, chỉ khó hiểu); sửa trong việc
   "Tăng cứng hub" của D.
7. **Chưa kiểm chứng:** tốc độ thật của một luồng TCP so với cách croc mở tới 8 kết nối dữ
   liệu. Mốc M3 đo việc này; nếu thiếu thì thêm "làn dữ liệu" (nhiều kết nối) ở Phase 2.

## 3. Kiến trúc

```
 người gửi ──1 luồng──► HUB (mở rộng của croc relay) ──sao chép──► người nhận 1..N
     │                    hàng đợi giới hạn cho mỗi người nhận
     └──── điều khiển: PAKE + dataKey + manifest, riêng từng người (khung DIRECT) ────┘
```

### 3.1 Luồng chạy

1. **Người gửi:** `croc send --receivers N FILE...` sinh code, in ra, kết nối relay vào phòng
   `multi:<RoomName>` với vai trò SENDER. **Người nhận:** `croc --multi CODE` (cờ rõ ràng cho
   MVP; tự nhận dạng code multi là việc Should) vào cùng phòng với vai trò RECEIVER.
2. **Phòng chờ:** mỗi người nhận vào thì người gửi chạy PAKE với người đó (khung `DIRECT`),
   rồi gửi `session` (manifest + `dataKey`, mã hóa bằng khóa cặp). Người gửi gửi N trong `JOIN`,
   nên **hub** giữ tối đa N người nhận cùng lúc; người thứ N+1 nhận `ERROR room-full`. Ai sai
   code hoặc không `ready` trong hạn thì bị `KICK`, nhường chỗ. Duyệt từng người (`--approve`)
   là việc Phase 2 (hiện báo "chưa hỗ trợ").
3. **Bắt đầu:** khi đủ N người sẵn sàng. (Nhấn Enter để bắt đầu sớm: chưa làm.) Từ `DATA` đầu
   tiên phòng bị khóa: người vào muộn nhận `ERROR room-locked`, kể cả người vừa rớt mạng muốn
   vào lại. Phase 2 thêm "vòng bù" để người vào muộn/lỗi nhận tiếp.
4. **Truyền:** người gửi đọc từng file, cắt khối 32 KB, mã hóa bằng `dataKey`, gửi khung
   `DATA`; hub phát cho mọi người nhận. Người nhận giải mã, ghi qua `receivefs`, kiểm tra
   băm cuối mỗi file.
5. **Kết thúc:** mỗi người nhận gửi `done` (hoặc `fail`) cho người gửi qua `DIRECT`. Người
   gửi thoát khi cả N có kết quả (mã 0 nếu tất cả `done`, 1 nếu có người lỗi, kèm danh sách
   lỗi); hub đóng phòng. Hiện người gửi chưa in trạng thái từng người (xem mục 11).
6. **Ai rời đi:** người nhận rời giữa chừng thì những người khác vẫn nhận tiếp, người gửi được
   báo `MEMBER_LEFT` và cuối cùng thoát mã 1; file dở của người đó bị xóa. Người gửi rời thì hub
   gửi `ERROR sender-gone` cho mọi người nhận và đóng phòng; file nào đã xong và khớp băm thì
   được giữ, file dở bị xóa.

### 3.2 Hành vi của hub (phía relay, chỉ thêm)

- File mới `src/tcp/multi_room.go` và **một nhánh** trong `clientCommunication` cho phòng
  `multi:`. Logic nằm ở gói mới `src/multihub` (không phụ thuộc mạng, test bằng đường truyền
  trong bộ nhớ).
- Phòng gồm `sender` và `receivers map[uint16]*member`. Mỗi người nhận có **hàng đợi gửi ra
  có giới hạn** (khoảng 8 MB). Khi một hàng đợi đầy, hub ngừng đọc từ người gửi (áp lực
  ngược của TCP), nên **người nhận chậm nhất quyết định tốc độ**: một người nhận không đọc
  làm **mọi người** dừng tới 30 giây. Hết 30 giây hub đóng kết nối người đó (không gửi
  `ERROR`, vì người không đọc cũng không nhận được) và báo người gửi `MEMBER_LEFT` lý do 1.
- Hub chỉ đọc phần **header không mã hóa** của khung.
- Giữ kết nối: hub gửi `PING` mỗi 15 giây, client trả `PING`; kết nối im lặng 60 giây bị đóng.
  Phòng `multi:` **không** dùng TTL phòng của relay 1-1: phòng sống tới khi người gửi rời.
- Cờ relay: `--multi-max-receivers` (mặc định 16), `--multi-queue-mb` (8),
  `--multi-stall-timeout` (30s), `--multi-join-rate` (120 lần/phút/IP). Khung ≤ 64 KB (kiểm
  tra trong `multiwire.Decode`). Không chặn theo IP (người cùng NAT phải vào được); chống làm
  phiền ở phía người gửi (`contracts.md` mục 3.5). Cờ `--max-rooms-open` của relay **không** đếm
  phòng `multi:` (chưa có giới hạn số phòng multi, việc của D ở Phase 2).
- Người nhận không được gửi `DATA`; `DIRECT` của người nhận chỉ đi tới người gửi.

## 4. Cấu trúc thư mục (mỗi người một vùng)

| Đường dẫn | Chủ | Nội dung |
|---|---|---|
| `src/multiwire/` | T | Hợp đồng dùng chung: khung hub, hằng số (loại khung, mã lỗi, vai trò, `SenderID`), `Conn`, `HubError`, bản ghi trong `DATA` (`record.go`) |
| `src/multisession/` | T | `Link` (kết nối đã `JOIN`, chia khung theo người), PAKE từng người nhận, chuyển `dataKey`, móc duyệt người vào; dùng chung cho hai engine |
| `src/multihub/` | D | Logic hub thuần |
| `src/tcp/multi_room.go` + 1 nhánh trong `src/tcp/tcp.go` | D | Gắn hub vào relay (chỗ duy nhất sửa code relay cũ) |
| `deploy/`, `tools/faultinject/` | D | Terraform + Ansible cho relay Azure, người nhận giả |
| `src/multisend/` | N | Đọc file, cắt khối, mã hóa một lần, phát, chờ N `done` (giới hạn tốc độ: chưa làm) |
| `src/cli/multi.go` + một dòng đăng ký trong `src/cli/cli.go` | N (gửi) / L (nhận) | Cờ `--receivers`, `--multi`, `--approve`, `--timeout` |
| `src/multirecv/` | L | Nhận, giải mã, ghi an toàn, kiểm tra băm, tiến độ |
| `scripts/bench/`, `docs/multi-e2e-test.md` | L | Benchmark và bài thử đầu-cuối từ xa qua relay |

Chỉ có `src/multiwire` và `src/multihub` là hợp đồng giữa nhiều người. Các phần còn lại độc
lập và dùng hub trong bộ nhớ (`multihub.Pipe`) để test.

Thực tế đến 21/11: `multiwire` và `multisession` do L viết cùng engine nhận (PR #12), không phải
T; PR #13 gom mọi hằng số, mã khung và định dạng bản ghi vào `multiwire` (trước đó hub, công cụ
và engine nhận mỗi nơi giữ một bản sao). Bảng trên vẫn là người chịu trách nhiệm review.

## 5. Phác thảo định dạng gói tin (T hoàn thiện thành `contracts.md` ở M0)

Nằm trong khung `comm` có sẵn (`"croc"` + độ dài u32 little-endian + nội dung).

**Header hub (không mã hóa):** `type u8 | flags u8 | from u16 | to u16 | payload`.

| `type` | Tên | Chiều | Ý nghĩa |
|---|---|---|---|
| 1 | `JOIN` | client → hub | Khai vai trò SENDER hoặc RECEIVER |
| 2 | `JOIN_ACK` | hub → client | Cấp mã thành viên; từ chối nếu phòng khóa/đầy/chưa có người gửi |
| 3 | `MEMBER_JOINED` / 4 `MEMBER_LEFT` | hub → người gửi | Có người vào/ra |
| 5 | `DIRECT` | hai chiều (người nhận ↔ người gửi) | Thông điệp điều khiển riêng |
| 6 | `DATA` | người gửi → hub → mọi người nhận | Khối dữ liệu đã mã hóa |
| 7 | `PING` | hai chiều | Giữ kết nối, phát hiện chết |
| 8 | `ERROR` | hub → client | Mã lỗi: `room-locked`, `room-full`, `no-sender`, `bad-frame`, `bad-version`, `sender-gone` (`kicked-slow` dự trữ) |
| 9 | `KICK` | người gửi → hub | Đóng ngay kết nối của một thành viên (người bị từ chối) |

**Thông điệp điều khiển** (JSON, trong `DIRECT`): `pake1`, `pake2`, `pake-confirm`,
`session` (manifest + `dataKey`, mã hóa bằng khóa cặp), `ready`, `done`, `fail`.

**Khối dữ liệu:** nội dung giải mã là một bản ghi: `chunk` = `kind u8 (1) | fileIdx u16 |
offset u64 | dữ liệu`, `file-end` = `kind u8 (2) | fileIdx u16 | sha256`, `all-end` = `kind u8
(3)` (`src/multiwire/record.go`); mã hóa AES-256-GCM bằng `dataKey` qua `crypt.EncryptAEADTo`
có sẵn (nonce 12 byte ngẫu nhiên do hàm tự sinh). Chi tiết bản ghi, băm cuối file và ghi chú an
ninh nằm trong `contracts.md`, mục 4.

## 6. Dòng lệnh

```
croc relay                                                      # relay/hub (thêm hỗ trợ multi:)
croc [--relay IP:9009] send --receivers N [--timeout 10m] FILE...
croc [--relay IP:9009] [--out THƯ_MỤC] --multi CODE             # MVP; sau này tự nhận dạng
```

- `--relay`, `--pass`, `--out`, `--debug` là cờ **toàn cục**: phải đứng **trước** `send`
  (`croc send --relay ...` báo `flag provided but not defined`).
- Mật khẩu relay: đặt `CROC_PASS` trong môi trường thay vì `--pass` (để không lộ trong lịch sử
  shell).
- Không có `--out` thì file lưu ở **thư mục đang đứng** khi chạy lệnh; thư mục `--out` phải có
  sẵn; file trùng tên đã có thì người nhận từ chối trước khi tải.
- `--code` tự đặt nên là ba từ (`apple-river-candle`); chuỗi kiểu `hello123` rơi về cách chia
  cũ theo ký tự và bí mật PAKE chỉ còn `23`. Bỏ `--code` để croc tự sinh.
- `--approve` hiện báo "chưa hỗ trợ" (Phase 2). `--timeout` hiện cắt cả phiên, xem mục 11.

## 7. Rủi ro và cách xử lý

| Rủi ro | Xử lý |
|---|---|
| Một luồng TCP chậm hơn nhiều kết nối của croc | M3 đo sớm; nếu thiếu thì "làn dữ liệu" là việc Must |
| Người nhận chậm kéo cả phiên | Hàng đợi giới hạn + loại sau 30 giây; ghi rõ là đánh đổi |
| Relay cũ làm người gửi treo | Bắt buộc chờ `JOIN_ACK`, báo lỗi rõ ràng |
| Xung đột sửa `tcp.go` / `cli.go` | D giữ đúng một nhánh trong `tcp.go`; phần CLI nằm trong `cli/multi.go`, `cli.go` chỉ thêm một dòng đăng ký, thống nhất ở M0 |
| Sai sót mật mã (nonce, khóa) | T chịu trách nhiệm `multisession`; dùng `crypt.EncryptAEADTo` (nonce ngẫu nhiên) và một `dataKey` mỗi phiên; người khác review |
| Con số hiệu năng không có thật | Chỉ nêu số do `scripts/bench` đo trên mạng thật |

## 8. Tiêu chí chấp nhận (chưa đo, sẽ xác nhận bằng bài thử)

- Với 1 người nhận, thời gian gần croc 1-1 trên cùng relay (mục tiêu ban đầu: chậm hơn không
  quá ~20%; đây là mục tiêu đặt ra, chưa phải số đã đo).
- Số byte người gửi tải lên xấp xỉ 1 lần kích thước file, dù N tăng.
- `sha256sum` giống nhau trên mọi máy nhận.
- `croc send` / `croc CODE` 1-1 vẫn chạy bình thường trên relay đã sửa.

## 9. Mốc thời gian (3 tuần, 4 người)

- **Tuần 1 (MVP):** M0 (ngày 1), M1a–M1d song song (ngày 2–4), M2 relay thật (ngày 5–6), M4 Azure và
  bài thử đầu-cuối từ xa (ngày 6–7). Cuối tuần 1 phải chạy được 1 gửi → 3 nhận qua Azure.
- **Tuần 2:** đo M3 qua Azure; Phase 2 Must: duyệt người vào + chặn người lạ, kéo thả, tiến độ,
  tăng cứng hub.
- **Tuần 3:** làn dữ liệu / vòng bù nếu cần, sửa lỗi, biểu đồ benchmark, chạy lại bài thử
  đầu-cuối; đóng băng trước hạn 2 ngày.
- Nếu chỉ có 2 tuần: giữ tuần 1 như trên; tuần 2 làm các việc Must của N và D, bỏ vòng bù.

## 10. Ghi chú vận hành

- Quy ước repo: không đẩy lên Git trừ khi được yêu cầu; khi đó dùng `~/dotfiles/git-pr`
  (xem `AGENTS.md`).
- Thử relay cục bộ: `croc relay`. Thử trên mạng thật: relay trên VM Azure do D dựng.

## 11. Vấn đề phát hiện khi chạy thử qua Azure (21/11/2026)

Trạng thái chi tiết từng mốc và hướng dẫn làm từng việc nằm ở [`phan-cong.md`](phan-cong.md).

Lần chạy đầu: 1 người gửi (file zip 1,48 GB) → 2 người nhận qua relay Azure. Cả hai việc dưới
đây đã được giao ở Phase 2 trong [`phan-cong.md`](phan-cong.md).

1. **`--timeout` cắt cả phiên, không chỉ phòng chờ (lỗi, D sửa).** Cờ được mô tả là "thời gian chờ
   người nhận" (mặc định 10 phút), nhưng `src/multisend/client.go` dùng `context.WithTimeout`
   bao quanh **toàn bộ** phiên: phòng chờ, tải lên và chờ `done`. Hết hạn giữa lúc tải lên thì
   kết nối bị đóng và người gửi chỉ báo
   `connection write failed: ... use of closed network connection`, không nói là hết giờ.
   File lớn qua mạng nhà gần như chắc chắn vượt 10 phút. Tạm thời: `--timeout 3h` hoặc
   `--timeout 0` (tắt).
2. **Không có giao diện trong terminal (thiếu tính năng, T làm).** Người gửi chỉ in code và một
   dòng "đã tải lên X byte" ở cuối: không biết ai đã vào, ai sẵn sàng, tiến độ hay tốc độ của
   từng người. Người nhận chỉ in `X / Y bytes`, không có % hay tốc độ. Phần ống dẫn đã có sẵn:
   người nhận đã gửi `progress` qua `DIRECT` và `multisend.SendOptions.Progress` đã tồn tại,
   nhưng `src/cli/multi.go` chưa gắn nó vào nên không có gì được vẽ.
