package reports

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFailure(t *testing.T) { path:=filepath.Join(t.TempDir(),"failure.json");if err:=WriteFailure(path,"retrieval","Environment","endpoint unavailable","retrieval metrics unavailable",time.Unix(1,0));err!=nil{t.Fatal(err)};b,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)};if string(b)==""{t.Fatal("empty failure report")} }
