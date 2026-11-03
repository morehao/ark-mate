package material

import "testing"

// TestValidateCarrier 表驱动：每种 node_kind 与载体的合法/非法组合（M-4 的纯值判据）。
func TestValidateCarrier(t *testing.T) {
	cases := []struct {
		name    string
		kind    NodeKind
		carrier Carrier
		wantErr bool
	}{
		{"folder 空载体合法", KindFolder, Carrier{}, false},
		{"folder 带 body 非法", KindFolder, Carrier{Body: "x"}, true},
		{"folder 带 table 非法", KindFolder, Carrier{TableID: "t"}, true},

		{"doc 有 body 合法", KindDoc, Carrier{Body: "# hi"}, false},
		{"doc 无 body 非法", KindDoc, Carrier{}, true},
		{"doc 带 drive 非法", KindDoc, Carrier{Body: "x", DriveUploadID: "u"}, true},

		{"table 有 table_id 合法", KindTable, Carrier{TableID: "libnd_x"}, false},
		{"table 无 table_id 非法", KindTable, Carrier{}, true},
		{"table 带 body 非法", KindTable, Carrier{TableID: "t", Body: "x"}, true},

		{"link 有 body+url 合法", KindLink, Carrier{Body: "snap", SourceURL: "https://a"}, false},
		{"link 缺 url 非法", KindLink, Carrier{Body: "snap"}, true},
		{"link 缺 body 非法", KindLink, Carrier{SourceURL: "https://a"}, true},

		{"drive 有 upload 合法", KindDrive, Carrier{DriveUploadID: "up"}, false},
		{"drive 无 upload 非法", KindDrive, Carrier{}, true},
		{"drive 带 body 非法", KindDrive, Carrier{DriveUploadID: "u", Body: "x"}, true},

		{"page 有 body 合法", KindPage, Carrier{Body: "<html>"}, false},
		{"page 无 body 非法", KindPage, Carrier{}, true},

		{"未知种类 fail-closed", NodeKind("weird"), Carrier{Body: "x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateCarrier(c.kind, c.carrier)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateCarrier(%s, %+v) err=%v，期望 wantErr=%v", c.kind, c.carrier, err, c.wantErr)
			}
		})
	}
}

// TestWouldCycle 防环判决：自己、子孙、无关节点、根。
func TestWouldCycle(t *testing.T) {
	// 树： A -> B -> C （B 的父是 A，C 的父是 B）；D 无关。
	ancestorsOfC := []string{"C", "B", "A"} // C 自身 + 向上到根
	cases := []struct {
		name      string
		moving    string
		newParent string
		ancestors []string
		wantCycle bool
	}{
		{"移到根不成环", "B", "", nil, false},
		{"移到自己是成环", "B", "B", []string{"B"}, true},
		{"移到自己的子孙成环（A 移到 C 下）", "A", "C", ancestorsOfC, true},
		{"移到无关节点不成环（D 移到 C 下）", "D", "C", ancestorsOfC, false},
		{"空移动不成环", "", "C", ancestorsOfC, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := WouldCycle(c.moving, c.newParent, c.ancestors)
			if got != c.wantCycle {
				t.Fatalf("WouldCycle(%q,%q,%v)=%v，期望 %v", c.moving, c.newParent, c.ancestors, got, c.wantCycle)
			}
		})
	}
}

// TestNodeKindSets 封闭集合自洽：子集关系与合法性。
func TestNodeKindSets(t *testing.T) {
	for _, k := range AllNodeKinds {
		if !IsValidNodeKind(k) {
			t.Fatalf("%s 应在合法集合内", k)
		}
	}
	if IsValidNodeKind("nope") {
		t.Fatalf("未知种类不应合法")
	}
	for _, k := range UploadKinds {
		if !IsUploadKind(k) {
			t.Fatalf("%s 应可上传", k)
		}
	}
	if IsUploadKind(KindFolder) {
		t.Fatalf("folder 是纯容器，不应走上传")
	}
}
