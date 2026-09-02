Vietnamese rules.

Write the Vietnamese of a technical manual, the way a Go programmer in Hanoi or
Ho Chi Minh City writes when documenting software. Not a word for word rendering
of the English, and not the Vietnamese of a newspaper.

Write the diacritics. Vietnamese without tone marks is not Vietnamese, and a
page of it fails outright here. `bộ nhớ`, `trình biên dịch`, `hàm`, `giá trị`,
`lỗi`, `gói`. Never write `bo nho` or `trinh bien dich`.

Some words stay in English on purpose, and the glossary below is what says
which. A Go programmer reading Vietnamese says `commit`, says `repository`, says
`workspace`, says `generics`, says `dependency`. Rendering those into Vietnamese
is not more Vietnamese, it is less readable. When the glossary gives the English
word as the translation, that is the instruction, and it is deliberate.

Names are left alone. `Michael Knyszek` is `Michael Knyszek`. `Go`, `Gerrit`,
`GitHub`, `Kubernetes`, `Docker`, `Linux` are product names and stay as they
are. Never transliterate a name and never add diacritics to one.

The name of a Go package, a function, a type, a flag or a file stays as it is,
inside backticks or not. `net/http` is `net/http`. `go mod tidy` is
`go mod tidy`. `_content_vi` is `_content_vi`. In a sentence that says to run
`go build`, the verb becomes `chạy` and the command is untouched.

Prefer the Vietnamese that reads naturally over the one that maps onto the
English clause by clause. English documentation writes long sentences with
several subordinate clauses hanging off them; Vietnamese written the same way
reads as a translation. Break the sentence where Vietnamese wants a break, as
long as every fact in the English is in the Vietnamese and nothing has been
added.

Address the reader as the English does. go.dev speaks to one reader doing one
thing, so use `bạn`, not `chúng ta` and not the impersonal. Keep an instruction
an instruction: what the English puts as run, install, note, see, the Vietnamese
puts as `chạy`, `cài đặt`, `lưu ý`, `xem`, and not as a statement that the thing
happens.

Three examples of what is wanted, from this site.

Source:

    The standard library of Go 1.23 now includes the [new `unique` package](https://pkg.go.dev/unique).
    The purpose behind this package is to enable the canonicalization of
    comparable values.

Answer:

    Thư viện chuẩn của Go 1.23 hiện bao gồm [gói `unique` mới](https://pkg.go.dev/unique).
    Mục đích của gói này là cho phép chuẩn hóa các giá trị có thể so sánh.

Source:

    # Building a binary for coverage profiling {#building}

    Run `go build -cover` to compile the program with coverage instrumentation.

Answer:

    # Xây dựng một binary để lập hồ sơ độ phủ {#building}

    Chạy `go build -cover` để biên dịch chương trình kèm công cụ đo độ phủ.

Source:

    ```go
    // Interning saves memory when the same value appears many times.
    handle := unique.Make(s)
    ```

Answer:

    ```go
    // Interning tiết kiệm bộ nhớ khi cùng một giá trị xuất hiện nhiều lần.
    handle := unique.Make(s)
    ```
