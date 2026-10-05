package packaging_test

import (
	"encoding/xml"
	"os"
	"testing"
)

func TestClassicExplorerMenuUsesOnlyClosedSupervisorGesturesAndUninstalls(t *testing.T) {
	data, err := os.ReadFile("windows/filees.wxs")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Package struct {
			Components []struct {
				ID     string `xml:"Id,attr"`
				Values []struct {
					Root, Key, Name, Value string `xml:",attr"`
				} `xml:"RegistryValue"`
				Removes []struct {
					Root, Key, Action string `xml:",attr"`
				} `xml:"RemoveRegistryKey"`
			} `xml:"Component"`
		} `xml:"Package"`
	}
	if err := xml.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	for _, component := range source.Package.Components {
		if component.ID != "FileESExplorerMenu" {
			continue
		}
		commands := map[string]string{}
		for _, value := range component.Values {
			if value.Root != "HKCU" {
				t.Fatal("not per-user")
			}
			commands[value.Key+"|"+value.Name] = value.Value
		}
		root := `Software\Classes\Directory\shell\FileES`
		for _, item := range []struct{ key, verb string }{{"01Add", "--explorer-add"}, {"02Detach", "--explorer-detach"}} {
			key := root + `\shell\` + item.key
			if commands[key+`\command|`] != `"[INSTALLFOLDER]filees-launch.exe" `+item.verb+` "%1"` {
				t.Fatal("unquoted or wrong gesture")
			}
			if commands[key+"|MultiSelectModel"] != "Single" {
				t.Fatal("not single folder")
			}
		}
		if len(component.Removes) != 1 || component.Removes[0].Root != "HKCU" || component.Removes[0].Key != root || component.Removes[0].Action != "removeOnUninstall" {
			t.Fatal("uninstall not scoped to our menu")
		}
		return
	}
	t.Fatal("missing classic menu")
}
