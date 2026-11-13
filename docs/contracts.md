# Hợp đồng giữa các thành phần (multi-send)

Tài liệu này chốt các giao diện giữa 4 người để làm song song. Đây là **bản khởi đầu do T
hoàn thiện ở mốc M0**; sau khi cả nhóm đồng ý thì coi là đóng băng. Mọi thay đổi sau đó phải
được cả D, N, T, L đồng ý (xem mục 9).

Nội dung dựa trên [`plan.md`](plan.md). Tên kiểu, hàm, lệnh giữ nguyên tiếng Anh. Số nguyên
nhiều byte dùng **little-endian**, giống `src/comm`.

Những chỗ ghi **(T xác nhận)** là quyết định chi tiết T chốt trong M0 rồi ghi lại vào đây.

## 1. Vận chuyển và tên phòng

- Mọi gói đi trong khung `comm` có sẵn: `"croc"` (4 byte) | độ dài u32 | nội dung. Nội dung
  tối đa **64 KB**.
- Client dùng lại bắt tay client–relay có sẵn: `tcp.ConnectToTCPServerControlContext(ctx,
  relayAddr, relayPass, room)`.
- **Tên phòng** gửi cho relay: `"multi:" + codephrase.Parse(code).RoomName`. Tiền tố `multi:`
  báo cho relay giao kết nối cho hub thay vì ghép cặp 2 người.
- Sau bắt tay, client gửi khung `JOIN` và **phải nhận `JOIN_ACK` trong 5 giây**; nếu không
  (relay cũ không hiểu `multi:`) thì báo: `relay này không hỗ trợ multi-send; dùng croc relay
  của bạn`.
- Phiên bản giao thức: **1**, nằm trong `JOIN`.

## 2. Khung hub (`src/multiwire`)

Nội dung gói là một khung hub:

```
type u8 | flags u8 | from u16 | to u16 | payload
```

- `from` do **hub điền** theo mã thành viên (client không tự khai).
- `to` chỉ dùng với `DIRECT`; các loại khác để 0.
- `flags` dành riêng, hiện bằng 0.
- Mã thành viên: **người gửi luôn là 1**, người nhận được cấp 2, 3, 4, ...

### 2.1 Loại khung

| `type` | Tên | Chiều | Payload |
|---|---|---|---|
| 1 | `JOIN` | client → hub | `version u8 \| role u8 (1=gửi, 2=nhận) \| maxReceivers u8` (`maxReceivers` chỉ có nghĩa với người gửi) |
| 2 | `JOIN_ACK` | hub → client | `version u8 \| memberID u16 \| maxReceivers u8` (`maxReceivers` là giá trị hub **thực sự áp dụng**; người gửi từ chối `--receivers` lớn hơn) |
| 3 | `MEMBER_JOINED` | hub → người gửi | `memberID u16 \| địa chỉ từ xa (chuỗi, độ dài u8)` |
| 4 | `MEMBER_LEFT` | hub → người gửi | `memberID u16 \| lý do u8` (0=thoát, 1=bị loại chậm, 2=lỗi mạng) |
| 5 | `DIRECT` | người nhận → người gửi, hoặc người gửi → một người nhận | Thông điệp điều khiển (mục 3), **tới nhận thì `to` = mã người nhận; từ người nhận thì hub tự đặt `to` = 1** |
| 6 | `DATA` | người gửi → hub → **mọi** người nhận | Bản mã (mục 4) |
| 7 | `PING` | hub ↔ client | Rỗng |
| 8 | `ERROR` | hub → client | `mã u8 \| thông báo UTF-8` |
| 9 | `KICK` | người gửi → hub | `memberID u16 \| lý do u8` (0=bị từ chối, 1=đã đủ người). Hub đóng kết nối đó ngay, nhả chỗ, không báo `MEMBER_LEFT` ngược lại cho người gửi. Người nhận gửi `KICK` thì hub trả `bad-frame` |

### 2.2 Mã lỗi (`ERROR`)

| Mã | Tên | Ý nghĩa |
|---|---|---|
| 1 | `room-locked` | Phòng đã bắt đầu gửi dữ liệu nên không nhận thêm người |
| 2 | `room-full` | Vượt số người nhận tối đa của hub |
| 3 | `no-sender` | Chưa có người gửi trong phòng |
| 4 | `kicked-slow` | Bị loại vì đứng im quá 30 giây |
| 5 | `bad-frame` | Khung sai định dạng, quá lớn hoặc không được phép |
| 6 | `bad-version` | Phiên bản giao thức không khớp |
| 7 | `sender-gone` | Người gửi đã thoát, phòng đóng |

### 2.3 Quy tắc của hub

- Chỉ người gửi được gửi `DATA`. Người nhận gửi `DATA` thì hub trả `bad-frame`.
- `DIRECT` từ người nhận chỉ tới người gửi; `DIRECT` từ người gửi tới đúng một người nhận.
  Người nhận **không** gửi được cho người nhận khác.
- **Người nhận vào trước khi có người gửi:** hub trả `no-sender`; client thử lại mỗi 1 giây
  trong tối đa 30 giây.
- **Khóa phòng ở hub:** chỉ một quy tắc tự động: sau khi người gửi gửi `DATA` đầu tiên, hub
  trả `room-locked` cho `JOIN` mới (người vào muộn cũng không giải mã được gì).
- **Chặn người lạ là việc của người gửi** (không có khung `LOCK`): người gửi duyệt từng
  người (`--approve`) và tự đếm số người đã duyệt. Khi từ chối một người, hoặc khi đã đủ
  `--receivers N`, người gửi gửi `reject` (`"refused"`/`"locked"`) qua `DIRECT` để người
  nhận thật thấy lý do, rồi gửi `KICK` để hub **đóng kết nối ngay**, kể cả khi client của
  người đó cố giữ máy. Chỉ `reject` thôi là chưa đủ vì nó chỉ là một thông điệp.
- Hub không tự khóa theo số người: chỗ trống do người bị `KICK` hoặc tự rời được nhả lại.
- Hết `--timeout` mà chưa bắt đầu thì người gửi thoát, hub đóng phòng (`sender-gone`).
- **Áp lực ngược:** mỗi người nhận có hàng đợi gửi ra khoảng **8 MB**. Khi bất kỳ hàng đợi
  nào đầy, hub ngừng đọc từ người gửi. Người nhận mà hàng đợi đầy và **không đọc được byte
  nào trong 30 giây** bị loại: hub gửi `ERROR kicked-slow` cho người đó, `MEMBER_LEFT` (lý do
  1) cho người gửi.
- `PING`: hub gửi mỗi 15 giây; không thấy dữ liệu nào từ một client trong 60 giây thì coi là
  chết. Client có thể gửi `PING` bất cứ lúc nào để giữ kết nối.
- Người gửi thoát: hub gửi `ERROR sender-gone` cho mọi người nhận rồi đóng phòng.
- Giới hạn cấu hình được trên `croc relay`: `--multi-max-receivers` (mặc định **16**),
  `--multi-queue-mb` (8), `--multi-stall-timeout` (30s); khung ≤ 64 KB.
- **Hub không phạt theo IP.** Nhiều người có thể chung một IP (NAT của trường, quán cà phê),
  và tên phòng là băm của code nên ai tới được phòng đều đã biết code; hub không có cách đúng
  để phân biệt người lạ với bạn bè. Chống làm phiền là việc của người gửi (mục 3.5).
- Phòng `multi:` vẫn chịu giới hạn lượt vào/phút theo IP của `src/tcp/defaults.go` như một
  lớp chống lũ kết nối để bảo vệ tài nguyên relay, ngưỡng nâng lên **120/phút** và đặt
  được bằng `--multi-join-rate`; nó không phải công cụ chặn người.

## 3. Thông điệp điều khiển (nằm trong `DIRECT`)

Dạng JSON, mỗi thông điệp có trường `t` (loại). Hình dạng giống `croc.SimpleMessage`
(`Bytes`, `Bytes2`, `Kind`, `Version`, `Curve`), nhưng gói trong khung `DIRECT`.

### 3.1 Trao khóa với từng người nhận (PAKE)

Dùng lại `src/pakekey`, **không sửa gói đó**:

- `pakekey.Init(passphrase, role, curve, pakekey.PurposeTransfer, room)` với:
  - `passphrase` = `codephrase.Parse(code).PAKEPassphrase`,
  - `curve` = `"p256"` (mặc định của croc),
  - `room` = `"multi:" + RoomName + "/" + memberID` (gắn phiên với đúng từng người nhận,
    nên hai người nhận khác nhau không thể dùng lại thông điệp của nhau),
  - vai trò: **người nhận là A (initiator), người gửi là B (responder)**, đúng với
    `pakekey.Identities`.
- Trình tự (T xác nhận chi tiết từng trường trong M0):

```
Người nhận → Người gửi : {"t":"pake1","version":2,"curve":"p256","bytes":"<A.Bytes()>","name":"binh-laptop"}
Người gửi  → Người nhận: {"t":"pake2","bytes":"<B.Bytes()>","bytes2":"<salt 32 byte>"}
Người nhận → Người gửi : {"t":"pake-confirm","tag":"<ConfirmationA>"}
```

- Hai bên gọi `pakekey.Derive(sharedKey, Context{Purpose, Room, Curve, Initiator,
  Responder, Salt})` để có `EncryptionKey` (**khóa cặp**) và hai thẻ xác nhận.
- Người gửi kiểm tra `pakekey.Confirm(ConfirmationA, tag)`. **Sai code phrase thì xác nhận
  thất bại:** người gửi gửi `{"t":"reject","reason":"auth"}` và bỏ qua mọi thông điệp tiếp theo
  từ mã đó. Mỗi người nhận chỉ có **một** lần thử mỗi kết nối.

### 3.2 Duyệt (tùy chọn)

Nếu người gửi bật `--approve`: sau khi xác thực xong, in
`<tên/IP> muốn vào, đồng ý? [y/N]` (qua `utils.GetInputContext`). Từ chối thì gửi
`{"t":"reject","reason":"refused"}`.

### 3.3 `session`: manifest và `dataKey`

Người gửi gửi `session`, **mã hóa bằng khóa cặp** (`crypt.Encrypt(plaintext, pairKey)`):

```json
{"t":"session",
 "confirm":"<ConfirmationB, người nhận phải kiểm tra>",
 "dataKey":"<32 byte ngẫu nhiên từ crypto/rand, base64>",
 "chunkSize":32768,
 "files":[{"idx":0,"path":"docs/a.pdf","size":123456}],
 "emptyDirs":["docs/empty"],
 "totalSize":123456}
```

- `path` là đường dẫn tương đối, dấu `/`. Người nhận chuẩn hóa bằng `receivefs.Normalize` và
  **từ chối** đường dẫn thoát thư mục, đường dẫn tuyệt đối, tên thiết bị Windows.
- `dataKey` là **một khóa cho cả phiên**, giống nhau cho mọi người nhận.
- Người nhận kiểm tra `confirm` (xác nhận người gửi biết code phrase) trước khi tin `session`.

### 3.4 Các thông điệp khác

| `t` | Chiều | Nội dung |
|---|---|---|
| `ready` | nhận → gửi | Đã kiểm tra thư mục đích, dung lượng, đường dẫn; sẵn sàng nhận |
| `progress` | nhận → gửi | `{"files":n,"bytes":m}`, gửi tối đa 2 lần/giây (để N hiện tiến độ từng người) |
| `done` | nhận → gửi | `{"ok":true,"bytes":m}` khi xong và băm khớp |
| `fail` | nhận → gửi | `{"reason":"hash-mismatch" \| "disk-full" \| "write-error" \| "..."}` |
| `reject` | gửi → nhận | `{"reason":"auth" \| "refused" \| "locked"}` |

Người gửi bắt đầu truyền khi đủ `N` người đã `ready` hoặc khi người dùng nhấn Enter (chỉ với
những người đã `ready`).

### 3.5 Chống làm phiền ở phía người gửi (`multisession`/`multisend`)

Mối đe dọa là code bị lộ. Không dùng IP để chặn; người gửi tự xử lý:
- Hạn **10 giây** cho PAKE của mỗi người vào; quá hạn thì `KICK` để không chiếm chỗ.
- Lời nhắc duyệt chỉ hiện **sau khi PAKE thành công**, xếp hàng và hiện từng cái một, cách
  nhau ít nhất 3 giây, kèm số người đang chờ.
- Trả lời `n` thì nhớ cặp (tên đã làm sạch, IP) cho cả phiên và lần sau tự từ chối, không nhắc.
- Đã đủ N người được duyệt: mọi người tới sau bị `reject` + `KICK` **tự động, không nhắc**.
- Kẻ đổi tên liên tục vẫn có thể vào lại, nhưng chỉ tốn một lần tự từ chối mỗi lần khi đã
  đủ N, hoặc một lời nhắc cách nhau 3 giây khi chưa đủ. Người gửi có thể thoát và dùng
  `--code` mới.

## 4. Khối dữ liệu (nằm trong `DATA`)

- Mã hóa **một lần** bằng `dataKey` với **AES-256-GCM**: `crypt.NewAESGCM(dataKey)` rồi
  `crypt.EncryptAEADTo(dst, plaintext, aead)`. Gói trên dây là `nonce(12) | bản mã | thẻ(16)`,
  với nonce ngẫu nhiên do hàm này tự sinh.
- Một `dataKey` chỉ dùng cho **tối đa 2^32 khối**; với khối 32 KB là hơn 100 TB, đủ dư cho
  mọi phiên.
- Khối rõ (trước khi mã hóa) là một bản ghi:

| `kind` | Nội dung |
|---|---|
| 1 `chunk` | `kind u8 \| fileIdx u16 \| offset u64 \| dữ liệu (≤ 32768 byte)` |
| 2 `file-end` | `kind u8 \| fileIdx u16 \| sha256 (32 byte)` |
| 3 `all-end` | `kind u8` |

- Kích thước khối: **32 KB** (`models.TCP_BUFFER_SIZE / 2`). Khung `DATA` luôn nhỏ hơn 64 KB.
- Người gửi tính **SHA-256** của từng file ngay khi đọc tuần tự và gửi trong `file-end`; không
  cần băm trước khi bắt đầu (tránh chờ lâu với file lớn).
- Người nhận ghi vào đúng `offset`, và sau `file-end` đối chiếu băm; sai băm thì **xóa file
  dở** và gửi `fail hash-mismatch`. Người nhận gửi `done` sau khi nhận `all-end` và mọi file
  khớp.
- **Ghi chú an ninh:** AAD hiện là rỗng (`crypt.EncryptAEADTo` không dùng AAD), nên header hub
  (`type/from/to`) không được xác thực mật mã; hub không đáng tin về siêu dữ liệu, nhưng nội
  dung được bảo vệ vì mọi thứ quan trọng (vị trí, chỉ số file, băm) nằm **bên trong** bản mã,
  và gửi lặp một khối là vô hại (ghi lại cùng dữ liệu). Hub làm rơi khối thì băm cuối file sẽ
  phát hiện.
- Phase 2 (vòng bù): thêm bản ghi báo đoạn thiếu và phát lại hợp các đoạn thiếu; định dạng
  mở rộng sẽ thêm vào đây khi làm.

## 5. Giao diện Go giữa các gói

Đây là các chữ ký đề xuất để mọi người code song song; T/D/N/L có thể tinh chỉnh trong M0,
nhưng sau M0 thì chỉ đổi theo mục 9.

### 5.1 `src/multiwire` (T)

```go
type Role uint8 // RoleSender = 1, RoleReceiver = 2

type Frame struct {
    Type    uint8
    Flags   uint8
    From    uint16
    To      uint16
    Payload []byte
}

func Encode(f Frame) ([]byte, error)   // lỗi nếu quá 64 KB
func Decode(b []byte) (Frame, error)
// hằng số: Version = 1, loại khung, mã lỗi, MaxFrame = 64*1024, ChunkSize = 32*1024
```

### 5.2 `src/multihub` (D)

```go
// Conn là một kết nối khung, trừu tượng hóa mạng để test trong bộ nhớ.
type Conn interface {
    Send(frame []byte) error
    Receive() ([]byte, error)
    Close() error
    RemoteAddr() string
}

type Config struct {
    MaxReceivers int           // mặc định 16, đặt bằng --multi-max-receivers
    QueueBytes   int           // mặc định 8 MB mỗi người nhận
    StallTimeout time.Duration // mặc định 30s
}

type Hub struct { /* ... */ }
func New(cfg Config) *Hub

// Serve chạy một kết nối cho tới khi nó kết thúc. Gọi một lần mỗi kết nối, sau
// khi bắt tay relay xong, với tên phòng đầy đủ ("multi:...").
func (h *Hub) Serve(room string, c Conn) error

// Pipe tạo cặp Conn nối nhau trong bộ nhớ, dùng cho test và hub trong bộ nhớ.
func Pipe() (a, b Conn)
```

Phía relay, `*comm.Comm` được bọc thành `Conn` ở `src/tcp/multi_room.go`.

### 5.3 `src/multisession` (T)

```go
// Dial kết nối relay, gửi JOIN, chờ JOIN_ACK (5 giây).
func Dial(ctx context.Context, relayAddr, relayPass, code string,
    role multiwire.Role, maxReceivers int) (*Link, error)

type Link struct { /* ... */ }
func (l *Link) MemberID() uint16
func (l *Link) Send(f multiwire.Frame) error
func (l *Link) Recv() (multiwire.Frame, error)
func (l *Link) Close() error
// Đuổi một người: l.Send(multiwire.Frame{Type: multiwire.TypeKick, Payload: ...}) (chỉ người gửi; mục 2.3).

// Phía người gửi: xác thực một người nhận (PAKE + duyệt), gửi session.
type ReceiverInfo struct{ ID uint16; Addr string; Name string } // Name đã làm sạch, chưa xác thực
func AuthorizeReceiver(ctx context.Context, l *Link, code string, id uint16,
    session Session, approve func(ReceiverInfo) bool) error

// Phía người nhận: PAKE, nhận và kiểm tra session.
func JoinSession(ctx context.Context, l *Link, code string) (Session, error)

type Session struct {
    DataKey   []byte
    ChunkSize int
    Files     []FileEntry
    EmptyDirs []string
    TotalSize int64
}
type FileEntry struct{ Idx int; Path string; Size int64 }
```

`multisession` cũng cung cấp bản hub trong bộ nhớ (dựa trên `multihub.Pipe()`) cho `--fake` khi chạy thử
mà chưa có relay.

### 5.4 `src/multisend` (N) và `src/multirecv` (L)

```go
// multisend
type SendOptions struct {
    Files      []string
    Receivers  int
    Approve    bool
    Timeout    time.Duration
    Relay      string
    RelayPass  string
    Code       string        // trống thì tự sinh
    ThrottleBps int64        // 0 = không giới hạn
    Progress   func(PeerProgress) // gọi mỗi khi có thay đổi từ từng người nhận
}
type PeerProgress struct {
    ID     uint16
    Name   string
    Bytes  int64
    State  string // joined, ready, receiving, done, failed, left
    Reason string
}
func Send(ctx context.Context, opts SendOptions) error

// multirecv
type ReceiveOptions struct {
    Code      string
    OutDir    string
    Relay     string
    RelayPass string
    Progress  func(bytes, total int64)
}
func Receive(ctx context.Context, opts ReceiveOptions) error
```

`Send` trả lỗi `nil` chỉ khi **cả N người nhận đều `done`**; nếu một số người lỗi thì trả lỗi
liệt kê họ (mã thoát CLI 1, mục 6).

## 6. Dòng lệnh

```
croc relay                                        # relay/hub, có sẵn, thêm hỗ trợ multi:
croc send --receivers N [--approve] [--timeout 10m] [--relay IP:9009] [--pass RELAYPASS]
          [--code CỤM_TỪ] FILE...                 # N ≥ 1 bật chế độ multi-send
croc --multi [--relay IP:9009] [--pass RELAYPASS] [--out THƯ_MỤC] CODE
```

- `--receivers N` (người gửi) vừa bật chế độ multi vừa là số người nhận cần duyệt; người thứ N+1 bị `reject`/`KICK`.
- `--name TÊN` (người nhận): nhãn hiển thị cho người gửi, mặc định là hostname.
- `--multi` (người nhận) là cờ rõ ràng cho MVP; tự nhận dạng code multi là việc Should.
- **Mã thoát:** 0 = mọi người nhận xong; 1 = có người nhận lỗi/bị loại/bị từ chối (liệt kê
  trên stderr); 2 = sai cách dùng.
- **Tiến độ người gửi (stderr), mỗi người một dòng**, cập nhật tại chỗ:

```
[2] An    80%  ready
[3] Bình  100% done
[4] Chi   ...  waiting
```

- **Tên hiển thị** do người nhận tự đặt (`--name`, mặc định hostname), gửi trong thông điệp
  `pake1` (`{"t":"pake1","name":"binh-laptop",...}`) nhưng chỉ được hiện **sau khi PAKE xong**,
  nên người không biết code không tạo được lời nhắc duyệt. Người gửi làm sạch: bỏ ký tự điều
  khiển và chuỗi escape, cắt còn 32 ký tự. Tên **không được xác thực** (ai cũng đặt được
  "Binh"), vì vậy lời nhắc luôn kèm IP:

```
"binh-laptop" (203.0.113.5) muốn vào, nhận? [y/N]
```

  Muốn chắc hơn thì so một mã ngắn (4 ký tự đầu của băm khóa cặp) qua kênh khác; để sau MVP.

## 7. Dùng lại từ croc (không sửa)

`src/comm`, `src/codephrase`, `src/pakekey`, `src/crypt`, `src/receivefs`, `src/termui`,
`tcp.ConnectToTCPServerControlContext`, `croc.GetFilesInfoContext`, `utils.GetInputContext`,
`golang.org/x/time/rate`.

**Chỉ sửa tối thiểu hai chỗ** trong code cũ: một nhánh `multi:` trong
`src/tcp/tcp.go` (D) và một dòng đăng ký lệnh trong `src/cli/cli.go` (N).

## 8. Điểm chưa chốt (T xác nhận trong M0)

1. Có thêm một `Purpose` riêng cho multi-send trong `src/pakekey` không? Mặc định **không**
   (dùng `PurposeTransfer` và gắn phiên bằng `room`), vì sửa `pakekey` đụng gói dùng chung.
2. ~~Cờ khóa phòng~~ đã chốt: không có `LOCK`; người gửi từ chối bằng `reject` + khung `KICK` (loại 9), mục 2.3; `flags` vẫn dự trữ.
3. Hình dạng chính xác các trường của `pake1/pake2/pake-confirm` so với `croc.SimpleMessage`.
4. ~~Tên người nhận~~ đã chốt: trong `pake1`, tối đa 32 ký tự, mục 6.

## 9. Thay đổi hợp đồng

1. Mở thay đổi trong `docs/contracts.md` (nhánh hoặc PR riêng).
2. Cả 4 người xem; nếu thay đổi chạm vào thư mục của ai thì người đó phải đồng ý.
3. Chủ thư mục cập nhật code và bản hub trong bộ nhớ **trong cùng ngày**; ai đang dùng giao diện cũ
   được báo lại.
