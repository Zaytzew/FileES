package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/internal/androidrelease"
	"filees/internal/releaseenvelope"
)

func TestAndroidMirrorPublishesItsOwnTree(t *testing.T) {
	key := newSigner(t)
	r := &repo{files: map[string][]byte{}, fetched: map[string]int{}}
	apk := []byte("apk-bytes")
	putAndroidRelease(t, r, key, "r9", 9, apk)
	out := t.TempDir()
	state := filepath.Join(t.TempDir(), "android-state.json")
	verifier := releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{"release-test": key.public}}
	changed, err := publishAndroid(context.Background(), r, verifier, "release-test", out, state)
	if err != nil || !changed {
		t.Fatalf("publish: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(out, "channels", "android.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "releases", "r9", "android", "filees-mobile-0.1.16.r9.apk")); err != nil {
		t.Fatal(err)
	}
	again, err := publishAndroid(context.Background(), r, verifier, "release-test", out, state)
	if err != nil || again {
		t.Fatalf("second publish: changed=%v err=%v", again, err)
	}
	putAndroidRelease(t, r, key, "r1", 1, []byte("older"))
	if _, err := publishAndroid(context.Background(), r, verifier, "release-test", out, state); err == nil {
		t.Fatal("older android sequence was accepted")
	}
}

func TestAndroidMirrorSkipsWhenTheChannelIsAbsent(t *testing.T) {
	r := &repo{files: map[string][]byte{}, fetched: map[string]int{}}
	_, err := publishAndroid(context.Background(), r, releaseenvelope.Ed25519Verifier{}, "release-test", t.TempDir(), filepath.Join(t.TempDir(), "state.json"))
	if !errors.Is(err, errAndroidUnpublished) {
		t.Fatalf("err = %v", err)
	}
}

func putAndroidRelease(t *testing.T, r *repo, key signer, id string, sequence uint64, apk []byte) {
	t.Helper()
	base := "releases/" + id + "/android/"
	name := "filees-mobile-0.1.16." + id + ".apk"
	if id == "r9" {
		name = "filees-mobile-0.1.16.r9.apk"
	}
	manifest, err := json.Marshal(androidrelease.Manifest{
		SchemaVersion: 1, ReleaseID: id, Platform: "android", Sequence: sequence, SecurityEpoch: 1,
		Version: "0.1.16." + id, APK: androidrelease.APK{Source: name, SHA256: digest(apk), Size: int64(len(apk))},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := json.Marshal(androidrelease.Channel{
		SchemaVersion: 1, ReleaseID: id, Manifest: base + "manifest.json", Sequence: sequence, SecurityEpoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.files[androidrelease.ChannelPath], r.files[androidrelease.ChannelPath+".sig"] = channel, key.sign(channel)
	r.files[base+"manifest.json"], r.files[base+"manifest.json.sig"] = manifest, key.sign(manifest)
	r.files[base+name] = apk
}
