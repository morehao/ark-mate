package session

import "testing"

// ——— v4 P2：队列行内编辑 + 附件往返（内存平面，纯域逻辑）———

// TestEditQueuedInputUpdatesInPlace 行内编辑：改正文/附件后队项**仍在原位**（不出队、不改顺序）。
func TestEditQueuedInputUpdatesInPlace(t *testing.T) {
	pl := NewPlane()
	q1, err := pl.EnqueueInput(nil, "c1", "第一条", DeliveryAfterRun, "run-1", 0, nil)
	if err != nil {
		t.Fatalf("enqueue1: %v", err)
	}
	q2, err := pl.EnqueueInput(nil, "c1", "第二条", DeliveryAfterRun, "run-1", 0, nil)
	if err != nil {
		t.Fatalf("enqueue2: %v", err)
	}

	ok, err := pl.EditQueuedInput(nil, "c1", q1.ID, "第一条（改）", []string{"att_a", "att_b"})
	if err != nil || !ok {
		t.Fatalf("edit 应命中并成功：ok=%v err=%v", ok, err)
	}
	list := pl.ListQueuedInputsOf(nil, "c1")
	if len(list) != 2 {
		t.Fatalf("编辑不该改变队列长度：got %d", len(list))
	}
	if list[0].ID != q1.ID || list[1].ID != q2.ID {
		t.Fatalf("编辑不该改变顺序：got [%s %s]", list[0].ID, list[1].ID)
	}
	if list[0].Text != "第一条（改）" {
		t.Fatalf("正文未更新：%q", list[0].Text)
	}
	if len(list[0].Attachments) != 2 || list[0].Attachments[0] != "att_a" {
		t.Fatalf("附件未更新：%+v", list[0].Attachments)
	}
	if list[0].Attachments == nil {
		t.Fatal("附件应非 nil")
	}
}

// TestEditQueuedInputNotFound 改一个不在队列里的 id → false（调用方据实报 40401）。
func TestEditQueuedInputNotFound(t *testing.T) {
	pl := NewPlane()
	if _, err := pl.EnqueueInput(nil, "c1", "x", DeliveryAfterRun, "", 0, nil); err != nil {
		t.Fatal(err)
	}
	ok, err := pl.EditQueuedInput(nil, "c1", "inp_nope", "y", nil)
	if err != nil || ok {
		t.Fatalf("未知队项应 ok=false err=nil：ok=%v err=%v", ok, err)
	}
}

// TestEncodeDecodeAttachments 附件 JSON 往返：空 → 空；正常列表保序；坏值 → nil（不 panic）。
func TestEncodeDecodeAttachments(t *testing.T) {
	if got := encodeAttachments(nil); got != "" {
		t.Fatalf("空列表应编码为空串，got %q", got)
	}
	if got := decodeAttachments(""); got != nil {
		t.Fatalf("空串应解码为 nil，got %v", got)
	}
	enc := encodeAttachments([]string{"att_a", "att_b"})
	dec := decodeAttachments(enc)
	if len(dec) != 2 || dec[0] != "att_a" || dec[1] != "att_b" {
		t.Fatalf("往返应保序：enc=%q dec=%v", enc, dec)
	}
	if got := decodeAttachments("{not-json"); got != nil {
		t.Fatalf("坏值应解码为 nil，got %v", got)
	}
}
