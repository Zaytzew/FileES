package client

import (
	"strings"
	"testing"
)

func TestLockReceiptRequiresBothTokensAndExactComment(t *testing.T) {
	good := `<status><target path="."><entry path="doc"><wc-status item="normal"><lock><token>token</token><owner>client</owner><comment>intent</comment></lock></wc-status><repos-status item="none"><lock><token>token</token><owner>client</owner><comment>intent</comment></lock></repos-status></entry></target></status>`
	if proof, err := parseLockReceipt(good, "intent"); err != nil || proof == nil || proof.Token != "token" {
		t.Fatalf("%+v %v", proof, err)
	}
	for _, bad := range []string{
		strings.Replace(good, "<token>token</token>", "<token>different</token>", 1),
		strings.Replace(good, "<owner>client</owner>", "<owner>different</owner>", 1),
		strings.Replace(good, "<comment>intent</comment>", "<comment>different</comment>", 1),
		strings.ReplaceAll(good, "repos-status", "ignored-status"),
		strings.ReplaceAll(good, "wc-status", "ignored-status"),
		strings.Replace(good, "</target>", `<entry path="extra"/></target>`, 1),
		"malformed",
	} {
		if proof, _ := parseLockReceipt(bad, "intent"); proof != nil {
			t.Fatalf("unproven receipt accepted: %s", bad)
		}
	}
}
