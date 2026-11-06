package main
import("fmt";"testing")
func TestDbgParse(t *testing.T){
 w:=nuRuntimeWorld(t.TempDir(),epGet(false))
 for _,s:=range []string{
  "apaga todos os arquivos .txt",
  "delete all text files in the current directory",
  "Remove regular files in the current directory tree",
  "remove todos os .tmp da pasta downloads",
 }{
  in:=nuGet().Parse(s,w)
  fmt.Printf("%q\n  → %s conf=%.2f slots=%v miss=%v alt=%v\n",s,in.Name,in.Confidence,in.Slots,in.Missing,in.Alt)
 }
}
