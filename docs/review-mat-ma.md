# Review mật mã multi-send (T, 28/11/2026)

Việc "Review mật mã" của T trong [`phan-cong.md`](phan-cong.md) (M1b và lịch tuần 2). Phạm vi:
`src/multisession` (PAKE từng người nhận, trao `dataKey`), `src/multiwire/record.go` (bản ghi
`DATA`), phần mã hóa trong `src/multisend/sender.go` và phần giải mã/kiểm tra trong
`src/multirecv/receive.go`, đối chiếu với [`contracts.md`](contracts.md) mục 3–4.

**Mô hình đe dọa** (theo `contracts.md`): code có thể bị lộ hoặc bị đoán; relay/hub chuyển
khung nhưng **không** được đọc nội dung và không đáng tin về siêu dữ liệu; người nhận đã xác
thực được tin là người được mời.

## Kết luận

Thiết kế mật mã đúng cho mục tiêu MVP: không ai thiếu code đọc được manifest hay dữ liệu, và
dữ liệu bị sửa trên đường đi đều bị phát hiện. Một lỗ hổng mức cao (đoán code không giới hạn)
**đã sửa trong PR này**. Hai điểm cần đổi hợp đồng được ghi lại làm đề xuất (mục "Phát hiện").

## Những gì đã kiểm tra và đúng

| # | Điểm | Ở đâu | Nhận xét |
|---|---|---|---|
| 1 | PAKE gắn với từng người nhận | `session.go`, `roomKey` | Phòng PAKE `multi:<RoomName>/<memberID>`, danh tính A/B qua `pakekey.Identities`; bản ghi của người này không dùng lại được cho người khác. |
| 2 | Dẫn xuất khóa | `pakekey.Derive` | HKDF-SHA256 trên khóa PAKE với muối 32 byte mới mỗi lần và toàn bộ transcript (mục đích, phòng, curve, hai giá trị PAKE, muối). Hai khóa xác nhận tách biệt A/B. |
| 3 | Xác nhận hai chiều | `AuthorizeReceiver`, `JoinSession` | Người gửi kiểm thẻ A **trước** khi gửi gì bí mật; người nhận kiểm thẻ B trước khi tin `session`. So sánh hằng thời gian (`hmac.Equal`). |
| 4 | Không đoán offline được | luồng `pake1/pake2/pake-confirm` | `session` chỉ được gửi sau khi thẻ A đúng, nên kẻ không biết code không có gì để thử offline. |
| 5 | Trao `dataKey` | `AuthorizeReceiver` | 32 byte `crypto/rand`, một khóa mỗi phiên (`multisend.Send`), chỉ đi trong `session` mã hóa AES-256-GCM bằng khóa cặp (nonce ngẫu nhiên, khóa cặp chỉ dùng một lần). |
| 6 | Mã hóa `DATA` | `sender.go`, `crypt.EncryptAEADTo` | AES-256-GCM, nonce 12 byte ngẫu nhiên mỗi bản ghi. 1 GB ≈ 33 000 bản ghi, rất xa giới hạn 2^32 lần mã hóa với một khóa (NIST SP 800-38D). Bộ đệm bản mã dùng lại an toàn vì `Link.Send` ghi đồng bộ dưới khóa. |
| 7 | Toàn vẹn luồng `DATA` | `receive.go` | Vị trí, chỉ số file, băm nằm **trong** bản mã. Người nhận từ chối khối nhảy cóc, so khớp khối trùng, kiểm SHA-256 ở `file-end`, và chỉ chấp nhận `all-end` khi mọi file đã khớp. Hub làm rơi, đảo, lặp hay cắt cụt đều bị phát hiện. Phát lại từ phiên khác không giải mã được (khóa khác). |
| 8 | Tên người nhận | `sanitizeName` | Bỏ mọi rune không in được (`unicode.IsPrint`): ESC, C0/C1 và cả ký tự định dạng như U+202E (đảo chiều chữ). Cắt 32 rune. Có test. |
| 9 | Kiểm tra phiên bản | `AuthorizeReceiver` | `pake1` phải đúng `version` và `curve` `p256`. |

## Phát hiện

### F1. Đoán code trực tuyến không giới hạn trong phòng chờ — **cao, đã sửa**

Code sinh ra là 3 từ EFF ngắn (1296 từ). Từ đầu chọn phòng (chỉ 1296 phòng, dễ dò ra phòng
đang mở); hai từ sau là mật khẩu PAKE, 1296² ≈ 1,7 triệu khả năng (~20,7 bit). PAKE chặn đoán
offline, nhưng mỗi lần vào là **một lần đoán online**. Ở croc 1-1, đoán sai một lần là phiên
hỏng. Còn ở multi-send, người gửi chỉ `KICK` rồi chờ tiếp, nên kẻ đoán vào lại được ngay, lặp
liên tục trong suốt phòng chờ (mặc định 10 phút, vô hạn với `--timeout 0`), và chiếm được tới N
chỗ song song. Ở vài lần thử mỗi giây mỗi chỗ, xác suất trúng trong một phiên đã lên tới mức
phần trăm.

**Đã sửa** (token bucket trên số lần sai code, `guessLimiter` trong `src/multisend/lobby.go`):
- `multisession.ErrAuthFailed` (lỗi riêng khi thẻ xác nhận sai), `multisession.ErrRefused` và
  `multisession.Reject`.
- Cho phép sai liên tiếp **10 lần** (gõ nhầm thật, kể cả cả lớp sau một NAT), sau đó hồi **1 lượt
  mỗi 30 giây**. Khi hết lượt, người mới vào nhận `reject` `busy` rồi bị `KICK` **trước** PAKE,
  nên không có lần đoán nào. Phòng chờ 10 phút cho tối đa 10 + 20 = 30 lần đoán (≈ 2·10⁻⁵).
- Người đã sẵn sàng không bị ảnh hưởng và phiên **không bị dừng**: kẻ đoán chỉ làm chậm người mới
  vào, không chấm dứt được việc gửi. Bản đầu của PR dừng cả phiên sau 10 lần sai; review cuối chỉ
  ra đó là lỗ từ chối dịch vụ (ai biết từ đầu của code cũng làm hỏng được phiên), nên đổi sang
  cách này.
- Không giới hạn theo IP: cả lớp sau NAT chung một IP, còn kẻ đoán thì dùng được nhiều IP.
- Vẫn đếm cả khi kẻ đoán ngắt kết nối ngay sau lần thử (`MEMBER_LEFT` tới trước kết quả PAKE).
- Test: `TestLobbyLimitsWrongCodes` (`src/multisend/progress_test.go`).

### F2. Thông điệp điều khiển sau PAKE không được xác thực — **trung bình, đề xuất**

`ready`, `progress`, `done`, `fail` (người nhận → người gửi) là JSON rõ trong `DIRECT`. Hub có
thể giả `done` cho một người nhận, chẳng hạn không chuyển `DATA` cho người đó mà vẫn báo "xong".
Khi đó người gửi in "2/2 xong" và thoát với mã 0 dù người nhận không có file. Bí mật dữ liệu
không bị ảnh hưởng; chỉ báo cáo giao nhận của người gửi là không đáng tin trước một relay xấu.

**Đề xuất** (đổi hợp đồng mục 3.4, chạm `multirecv` của L và `multisend` của N nên cần đồng ý
theo mục 9): `done` kèm
`proof = HMAC-SHA256(dataKey, "croc-multi done v1" ‖ memberID u16 ‖ sha256 của từng file)`.
Người gửi kiểm bằng `hmac.Equal`. Hub không có `dataKey` nên không giả được, và `memberID` ngăn
chép bằng chứng của người này sang người khác. Chưa làm trong PR này để không phá tương thích
ngay trước lượt chạy ma trận đầu-cuối.

### F3. Khóa nhóm: người nhận này có thể giả dữ liệu cho người nhận khác — **thấp, giới hạn thiết kế**

Mọi người nhận đã xác thực đều có `dataKey`, và SHA-256 trong `file-end` nằm dưới cùng khóa đó.
Một người nhận xấu **thông đồng với relay** có thể dựng `DATA` giả mà người nhận khác chấp nhận.
Hub hiện chỉ nhận `DATA` từ người gửi, nên riêng một người nhận thì không làm được.

**Đề xuất sau MVP:** người gửi sinh khóa Ed25519 mỗi phiên, gửi khóa công khai trong `session`
(đã mã hóa bằng khóa cặp nên xác thực), rồi ký `file-end` và `all-end`. Chi phí: một chữ ký mỗi
file.

### F4. Tên người nhận gửi rõ trong `pake1` — **thấp, đề xuất**

Relay đọc được hostname mặc định của người nhận (lộ thông tin) và có thể sửa tên. Tên vốn không
được xác thực, và lời nhắc duyệt luôn kèm IP (`contracts.md` mục 6), nên ảnh hưởng nhỏ. Có thể
chuyển tên vào một trường mã hóa bằng khóa cặp sau PAKE.

### F5. Ghi chú, không cần sửa

- AAD rỗng: chấp nhận được, đã ghi trong `contracts.md` mục 4, vì mọi siêu dữ liệu quan trọng
  nằm trong bản mã.
- `reject` không được xác thực, nên relay có thể giả để làm hỏng một lần vào. Relay vốn có thể
  làm rơi khung, nên đây không phải quyền mới.

## Review cuối (28/11, việc tuần 3 của T)

Rà lỗi logic trên `src/multihub`, `src/multisession`, `src/multisend`, `src/multirecv`,
`src/multiwire`, `src/cli/multi.go`, `src/cli/multi_ui.go`, `src/tcp/multi_room.go`.

| Phát hiện | Mức | Xử lý |
|---|---|---|
| Giới hạn 10 lần sai code dừng cả phiên (lỗ từ chối dịch vụ, lớp đông gõ nhầm cũng dừng) | quan trọng | **Đã sửa:** token bucket, xem F1 |
| Người nhận rớt giữa lúc đóng phòng chờ và lúc bắt đầu tải lên bị hiện là "rời phòng chờ", thiếu trong tóm tắt và dòng `bench:` | quan trọng | **Đã sửa:** báo `sending` trước khi chạy goroutine theo dõi |
| "bắt đầu gửi" in ở lần `ready` thứ N dù phòng chờ có thể còn mở | nhỏ | **Đã sửa:** in ở sự kiện `sending` |
| Dòng tiến độ cắt theo số rune nên vẫn tràn dòng với tên chữ rộng (CJK) | nhỏ | **Đã sửa:** cắt theo độ rộng hiển thị (`golang.org/x/text/width`) |
| `croc send --receivers N --text ...` qua được kiểm tra rồi báo lỗi khó hiểu | có từ trước | **Đã sửa:** báo rõ "multi-send chưa hỗ trợ --text" |
| Với `--approve`, dòng sự kiện có thể chen vào giữa lời nhắc y/N | nhỏ | **Chưa sửa:** cần phối hợp lời nhắc (vùng của N) với UI; lời nhắc vẫn đúng người, chỉ bị xuống dòng |

Đã kiểm: hub không dùng lại mã thành viên (`src/multihub/hub.go`, mã chỉ tăng), nên kết quả PAKE
đến muộn không bị gán nhầm cho người mới.
