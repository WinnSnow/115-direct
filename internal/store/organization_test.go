package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOrganizationRecordsHistoryPaginationAndSecretProjection(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	for i := 0; i < 510; i++ {
		id := fmt.Sprint(i)
		raw, _ := json.Marshal(map[string]any{"content": "https://gateway/direct?sig=TOPSECRET", "config": map[string]string{"token": "TOPSECRET"}, "item": organizationItem{Media: MediaEntry{ID: "play-" + id, RemoteID: id, Name: "Film" + id}, Link: MediaLink{RemoteID: id, SourcePath: "/pending/" + id, OutputPath: "/output/" + id, Mode: "hardlink", VersionGroup: "movie:603:0:0-0:"}}})
		if err := st.PutExecution(ctx, Execution{ID: "organize:job:" + id, Kind: "organize", Status: "completed", Body: raw}); err != nil {
			t.Fatal(err)
		}
	}
	records, total, err := st.OrganizationRecords(ctx, RecordFilter{Status: "success", Limit: 25, Offset: 500})
	if err != nil || total != 510 || len(records) != 10 {
		t.Fatalf("%d %d %v", len(records), total, err)
	}
	raw, _ := json.Marshal(records)
	if strings.Contains(string(raw), "TOPSECRET") || records[0].Mode != "hardlink" || !strings.HasPrefix(records[0].FileID, "play-") {
		t.Fatalf("unsafe or incomplete records: %s", raw)
	}
	records, total, err = st.OrganizationRecords(ctx, RecordFilter{Search: "/output/509"})
	if err != nil || total != 1 || records[0].RemoteID != "509" {
		t.Fatalf("search %v %d %v", records, total, err)
	}
}

func TestOrganizationRecordsUnrecognizedFailuresAndSkippedFiles(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	for _, status := range []string{"waiting_match", "failed", "queued", "completed"} {
		id := status
		m := MediaEntry{ID: "play-" + id, RemoteID: id, Name: "Film", STRMPath: "/pending/" + id}
		l := MediaLink{RemoteID: id, SourcePath: m.STRMPath, IngestedAt: time.Now()}
		if err := st.PutMedia(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := st.PutLink(ctx, l); err != nil {
			t.Fatal(err)
		}
		ids, _ := json.Marshal([]string{id})
		j := TransferJob{ID: "job-" + id, Source: "local", Status: status, Expected: ids}
		if status == "failed" {
			j.Error = "Jellyfin 更新通知失败"
		}
		if err := st.CreateJob(ctx, &j); err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateJob(ctx, &j); err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			p := organizationPlan{Items: []organizationItem{{Media: m, Link: l, Skip: "保留已有版本"}}}
			if err := st.PutSetting(ctx, "organize_plan:"+j.ID, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, state := range []string{"unrecognized", "failed", "queued", "skipped"} {
		rs, n, err := st.OrganizationRecords(ctx, RecordFilter{Status: state})
		if err != nil || n != 1 || len(rs) != 1 {
			t.Fatalf("%s %v %d %v", state, rs, n, err)
		}
		if state == "failed" && rs[0].Error == "" {
			t.Fatal("failure reason missing")
		}
	}
}

func TestOrganizationRecordRetainsInboxIDForLocalJobMapping(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	m := MediaEntry{ID: "play-child", RemoteID: "child", Name: "feature.strm", STRMPath: "/pending/feature.strm"}
	l := MediaLink{RemoteID: "child", InboxID: "parent-directory", SourcePath: "/pending/feature.strm", OutputPath: "/library/feature.strm", IngestedAt: time.Now()}
	if err := st.PutMedia(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLink(ctx, l); err != nil {
		t.Fatal(err)
	}
	ids, _ := json.Marshal([]string{"parent-directory"})
	j := TransferJob{ID: "job-parent", Source: "local", Status: "completed", Expected: ids}
	if err := st.CreateJob(ctx, &j); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJob(ctx, &j); err != nil {
		t.Fatal(err)
	}
	records, total, err := st.OrganizationRecords(ctx, RecordFilter{Limit: 100})
	if err != nil || total != 1 || len(records) != 1 {
		t.Fatalf("records=%+v total=%d err=%v", records, total, err)
	}
	if records[0].RemoteID != "child" || records[0].InboxID != "parent-directory" || records[0].SourcePath != l.SourcePath || records[0].OutputPath != l.OutputPath {
		t.Fatalf("record mapping lost: %+v", records[0])
	}
}

func TestOrganizationRecordProjectsCompletedCloudJobFromInboxID(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	m := MediaEntry{ID: "play-cloud", RemoteID: "cloud-file", Name: "Film.mkv", RemotePath: "电影/Film.mkv", STRMPath: "/library/Film.strm"}
	l := MediaLink{RemoteID: m.RemoteID, InboxID: "cloud-stage", SourcePath: "/pending/Film.strm", OutputPath: m.STRMPath, Kind: "movie", TMDBID: 603, IngestedAt: time.Now()}
	if err := st.PutMedia(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLink(ctx, l); err != nil {
		t.Fatal(err)
	}
	j := TransferJob{ID: "cloud-job", Source: "web", Status: "completed", StageCID: "cloud-stage", Title: "Film"}
	if err := st.CreateJob(ctx, &j); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJob(ctx, &j); err != nil {
		t.Fatal(err)
	}
	records, total, err := st.OrganizationRecords(ctx, RecordFilter{Search: j.ID})
	if err != nil || total != 1 || len(records) != 1 {
		t.Fatalf("records=%+v total=%d err=%v", records, total, err)
	}
	if records[0].TaskID != j.ID || records[0].ID != "job:"+j.ID+":"+m.RemoteID || records[0].Status != "success" {
		t.Fatalf("cloud task projection lost: %+v", records[0])
	}
}
