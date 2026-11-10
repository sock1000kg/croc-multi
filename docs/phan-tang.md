# croc hoạt động thế nào qua từng tầng

Tài liệu này giải thích, bằng ngôn ngữ thường, croc làm gì ở từng tầng mạng để
gửi một file từ máy này sang máy kia an toàn và nhanh. Người đọc không cần biết
trước các kỹ thuật bên dưới — mỗi thuật ngữ đều được giải thích khi xuất hiện.

> **Ghi chú về độ chính xác.** Bảng tóm tắt ban đầu có vài chi tiết khác với mã
> nguồn hiện tại; các chỗ đó được sửa ngay trong bài và gom lại ở mục
> [Những điểm cần lưu ý](#những-điểm-cần-lưu-ý).

## Bức tranh tổng thể

Khác với FTP hay HTTP upload/download (một bên là server giữ file, bên kia là
client), croc kết hợp hai mô hình:

- **Client–Server cho pha "làm quen" (signaling).** Hai máy chưa biết địa chỉ
  của nhau, nên cả hai cùng kết nối tới một máy chủ trung gian gọi là **relay**.
  Relay chỉ đóng vai "bàn lễ tân": ghép hai người dùng cùng một mã lại với nhau.
- **P2P cho pha truyền dữ liệu.** Khi đã quen nhau, hai máy cố nói chuyện
  **trực tiếp**. Chỉ khi không đi thẳng được thì dữ liệu mới đi vòng qua relay.

Dù đi đường nào, relay (hoặc bất kỳ ai nghe lén trên đường) cũng **không đọc
được nội dung file**, vì dữ liệu đã được mã hóa từ đầu đến cuối.

```
 Người gửi ──(1) cùng mã "3 từ"──► Relay ◄── Người nhận
     │                                          │
     └────────(2) đi thẳng nếu được─────────────┘
              (hoặc vòng qua relay nếu không)
```

---

## Tầng Ứng dụng: bảo mật và chia nhỏ file

Đây là tầng "nội dung": croc làm gì với chính dữ liệu của bạn.

### 1. PAKE: biến mật khẩu ngắn thành khóa mạnh

Khi gửi file, croc cho bạn một mã như `1234-tao-cam-xe`. Mã này rất ngắn so với
một khóa mã hóa thật (thường dài 256 bit). Nếu dùng thẳng mã này làm khóa, kẻ
tấn công chỉ cần thử đoán dần.

**PAKE** (Password-Authenticated Key Exchange — trao đổi khóa có xác thực bằng
mật khẩu) giải quyết việc này. Hiểu đơn giản: hai bên trao đổi vài thông điệp
toán học, và cuối cùng **cả hai cùng ra một khóa dài, ngẫu nhiên, dùng một lần**
— nhưng chỉ khi họ nhập cùng một mã. Người đứng giữa nghe được các thông điệp
cũng không suy ra được khóa, và chỉ có một lần thử đoán cho mỗi phiên (đoán sai
là phiên hỏng, phải làm lại từ đầu).

croc dùng thuật toán SPAKE2 trên đường cong elliptic (mặc định `p256`, có thể
đổi bằng `--curve`).

### 2. Mã hóa dữ liệu

Có khóa rồi, mọi khối dữ liệu được mã hóa bằng **AES-256-GCM**. Chữ "GCM" nghĩa
là ngoài việc che nội dung, nó còn kèm một "con dấu" để người nhận phát hiện nếu
dữ liệu bị sửa trên đường đi. Sai một bit là khối đó bị từ chối.

### 3. Chia nhỏ (chunking) và nén

File không được gửi thành một khối khổng lồ mà cắt thành nhiều **khối nhỏ**
(trong code hiện tại là khoảng 32 KB mỗi khối). Lợi ích:

- Có thể gửi nhiều khối **song song** (xem tầng Giao vận).
- Mỗi khối được mã hóa và kiểm tra độc lập.
- Nếu đứt giữa chừng, chỉ cần gửi lại các khối còn thiếu.

Trước khi mã hóa, khối có thể được **nén** để tiết kiệm băng thông (với file đã
nén sẵn như `.zip`, `.mp4` thì gần như không lợi, croc vẫn xử lý bình thường).

### 4. Resume (tiếp tục khi bị gián đoạn)

Máy nhận ghi nhớ nó **đã nhận khối nào** (một dạng "danh sách/manifest"). Nếu
mạng rớt rồi bạn chạy lại cùng lệnh, hai bên so khớp danh sách và chỉ truyền
những khối chưa có, thay vì gửi lại từ đầu. Có kiểm tra băm (hash) để chắc là
phần đã có đúng là phần của file đó.

---

## Tầng Giao vận: tìm nhau và dùng hết băng thông

Tầng này lo chuyện "đưa các khối đi bằng đường ống nào".

### 1. UDP broadcast/multicast: tìm nhau trong mạng nội bộ

Nếu hai máy **cùng mạng LAN** (cùng Wi-Fi/nhà/văn phòng), việc vòng ra Internet
là lãng phí. Máy nhận croc "hét" một câu lên mạng LAN: *"có ai đang gửi file với
mã này không?"* (dùng **UDP multicast**, địa chỉ mặc định `239.255.255.250`).
Nếu máy gửi nghe thấy, hai bên biết địa chỉ nội bộ của nhau và nối thẳng, tốc độ
tận dụng cả mạng LAN, không phụ thuộc đường Internet.

UDP ở đây hợp lý vì tin nhắn ngắn, "hét rồi chờ trả lời", không cần thiết lập
kết nối trước.

### 2. Nhiều kết nối TCP song song

Một kết nối TCP đơn lẻ thường không chạy hết tốc độ đường truyền, nhất là khi
đường dài, độ trễ cao: nó phải chờ xác nhận trước khi gửi tiếp. Giải pháp là mở
**nhiều kết nối cùng lúc** và chia các khối cho chúng gửi song song, giống mở
nhiều quầy thu ngân thay vì một. Số kết nối phụ thuộc số cổng của relay (mặc
định relay dùng 5 cổng, 9009–9013) và tối đa 8.

Cổng **9009** là cổng mặc định của relay: nơi hai bên gặp nhau ở pha "làm quen".

---

## Tầng Mạng & Liên kết: vượt qua router để nối trực tiếp

Đây là phần khó nhất, vì phần lớn máy tính ở nhà nằm sau router (**NAT**) và
không có địa chỉ Internet riêng để người khác kết nối vào.

### Vấn đề: NAT

Router nhà bạn cho phép bạn **gọi ra** ngoài, nhưng chặn người lạ **gọi vào**.
Nếu cả người gửi và người nhận đều sau NAT thì không ai gọi được ai.

### 1. Biết địa chỉ công khai của mình (STUN)

Máy của bạn chỉ biết địa chỉ nội bộ (kiểu `192.168.1.5`). Nó cần biết "ngoài
Internet người ta thấy mình là địa chỉ nào". Kỹ thuật phổ biến là hỏi một máy
chủ bên ngoài: *"bạn thấy gói tin của tôi đến từ địa chỉ nào?"* — giao thức đó
tên là **STUN**.

### 2. Đục lỗ NAT (hole punching)

Khi biết địa chỉ công khai của nhau, hai máy **cùng lúc** gửi gói tin về phía
nhau. Router của mỗi bên thấy "máy trong nhà mình vừa gửi ra địa chỉ đó", nên
mở một "lỗ" cho gói tin trả lời đi vào. Vậy là hai bên nói chuyện trực tiếp được
dù cả hai đều sau NAT.

### 3. Dự phòng qua relay

Một số loại NAT (hay gặp là **symmetric NAT**, thường ở mạng di động hoặc công
ty) đổi địa chỉ công khai theo từng đích đến, nên "đục lỗ" thất bại. Khi đó
croc **chuyển dữ liệu qua relay** — chậm hơn một chút nhưng luôn chạy được.
Nhờ mã hóa đầu cuối, relay chỉ chuyển tiếp các khối đã mã hóa mà không đọc được.

### croc làm điều này như thế nào trong code

croc **không tự gọi STUN và không tự đục lỗ**: trong mã nguồn không có đoạn nào
làm việc đó, nên bạn sẽ không thấy log "kiểm tra STUN" của croc. Phần này được
giao cho thư viện **Tailcat**, một lớp mạng dựa trên công nghệ của Tailscale
(WireGuard chạy ngay trong chương trình, không cần cài VPN). STUN và đục lỗ nằm
bên trong thư viện đó, nên log của croc không hiển thị chúng.

Ở chế độ mặc định (`--transport auto`), sau khi bắt tay PAKE, hai bản croc
native dùng Tailcat như sau:

1. Bắt đầu kết nối qua máy chủ trung chuyển **DERP** (giải thích ngay bên dưới),
2. Rồi cố gắng **nâng cấp lên đường trực tiếp** nếu đục lỗ NAT thành công.

Nếu đối phương là trình duyệt hoặc bản croc cũ, hoặc Tailcat không kịp thiết lập
trong thời hạn cho phép, croc quay về cách truyền cổ điển qua các cổng của relay.
Có thể chọn cách truyền bằng `--transport` (xem README, mục "Data transport
selection").

### DERP là gì và tại sao bắt đầu bằng nó?

**DERP** (Designated Encrypted Relay for Packets) là hệ thống máy chủ chuyển tiếp
do Tailscale vận hành trên nhiều khu vực địa lý. Nó giống relay của croc ở chỗ chỉ
chuyển các gói đã mã hóa, nhưng khác ở chỗ hai máy **chỉ cần kết nối ra ngoài
(outbound) tới DERP**, nên đi qua được hầu hết tường lửa và NAT.

Lý do bắt đầu bằng DERP thay vì đợi đục lỗ xong mới gửi:

- **Đục lỗ cần thời gian và có thể thất bại.** Hai bên phải hỏi địa chỉ công khai,
  trao đổi, rồi thử gửi gói qua lại vài lần. Trong lúc đó chưa thể truyền gì.
- **DERP luôn thông ngay.** Nên kết nối được thiết lập gần như tức thì, bất kể
  loại NAT.
- **Nâng cấp ngầm.** Trong khi dữ liệu chạy qua DERP, hai máy vẫn âm thầm thử đục
  lỗ. Khi thành công, đường truyền tự chuyển sang trực tiếp (DERP chỉ còn là dự
  phòng) mà người dùng không thấy gián đoạn.

Nói ngắn gọn: DERP đảm bảo "luôn kết nối được", đục lỗ lo phần "nhanh nhất có thể".

### Hai kiểu "race" bạn có thể thấy trong log

Khi bật debug, bạn có thể thấy croc dường như "đua" nhiều thứ. Có hai chỗ khác nhau:

1. **Chọn relay công cộng (`src/publicrelay`).** Nếu bạn không chỉ định relay, croc
   gửi một gói ping tới cả bốn relay `1.getcroc.com` … `4.getcroc.com` cùng lúc và
   **dùng relay trả lời đầu tiên**, hủy các lần thử còn lại. Đây là việc chọn *một*
   relay tốt nhất cho pha làm quen, không phải truyền song song.
2. **Tailcat so với relay.** Tailcat có thời hạn thiết lập; quá hạn thì croc yêu cầu
   hai bên dùng relay dự phòng.

Việc truyền song song là chuyện khác: sau khi chọn xong **một** relay, croc mở
đồng thời **tất cả** các cổng dữ liệu của relay đó (9009–9013, tối đa 8) và chia
các khối cho chúng. Các cổng này không đua nhau; chúng cùng làm việc.

---

## Tóm tắt một lần gửi file

1. Người gửi chạy `croc send file` → nhận mã 3 từ, đăng ký với **relay**.
2. Người nhận nhập mã → relay ghép hai bên (**client–server**).
3. Hai bên chạy **PAKE** từ mã đó → có chung khóa mã hóa.
4. Thử đường nhanh nhất: **cùng LAN** (UDP multicast) → **đục lỗ NAT** → cuối cùng
   mới **qua relay**.
5. File được **chia khối, nén, mã hóa AES-GCM**, gửi qua **nhiều kết nối song
   song** (**P2P** nếu đi thẳng được).
6. Nếu đứt, chạy lại: chỉ các khối còn thiếu được gửi (**resume**).

## Những điểm cần lưu ý

So với bảng tóm tắt ban đầu, theo mã nguồn hiện tại:

| Bảng ban đầu | Thực tế trong code |
| --- | --- |
| Khối 1 MB | Khối khoảng 32 KB (`models.TCP_BUFFER_SIZE / 2`) |
| 4–8 kết nối TCP | Theo số cổng relay (mặc định 5), tối đa 8 |
| STUN + đục lỗ NAT | Do Tailcat/Tailscale (magicsock + DERP) đảm nhiệm, không phải code riêng của croc |
| Dữ liệu luôn P2P | P2P khi cùng LAN hoặc đục lỗ thành công; còn lại đi qua relay hoặc DERP |
| UDP Port 9009 | Cổng 9009 là cổng **TCP** của relay; phát hiện LAN dùng UDP multicast riêng |
