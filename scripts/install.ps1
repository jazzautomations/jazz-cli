# jazz-cli installer for Windows (no admin, no WSL):
#   irm https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.ps1 | iex
#
# Downloads jazz-windows-<arch>.exe (amd64/arm64), verifies the release
# signature (manifest.txt.sig, Ed25519, key embedded below by
# scripts/release.sh) and the binary's sha256 from the signed manifest, and
# installs it to %LOCALAPPDATA%\Programs\jazz, adding that folder to the user
# PATH. The Ed25519 check is plain PowerShell (System.Numerics), so it needs
# no minisign or OpenSSL and works on Windows PowerShell 5.1 and PowerShell 7.
# An installer without a key (development copy) checks only the sha256 and
# warns; $env:JAZZ_REQUIRE_SIG = '1' makes that an error.
#
#   $env:JAZZ_BASE_URL   folder with manifest.txt(.sig) and the binaries
#                        (default: latest GitHub release of jazzautomations/jazz-cli)
#   $env:JAZZ_BIN_DIR    install folder (default: %LOCALAPPDATA%\Programs\jazz)
#   $env:JAZZ_ARCH       amd64 | arm64 (default: this machine)
#   $env:JAZZ_NO_PATH    '1' = do not touch the user PATH

# filled in by scripts/release.sh (base64 Ed25519 public key)
$JazzPubKey = ''

& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue' # Windows PowerShell 5.1 downloads crawl with the progress bar
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}
    try { Add-Type -AssemblyName System.Numerics } catch {}

    function Fail([string]$msg) { throw "jazz: $msg" }
    # Get-FileHash sits in a module that Windows PowerShell 5.1 cannot autoload when PSModulePath was inherited from pwsh 7; use .NET directly
    function Get-Sha256Hex([string]$path) {
        $sha = [System.Security.Cryptography.SHA256]::Create()
        $fs = [System.IO.File]::OpenRead($path)
        try { return ([System.BitConverter]::ToString($sha.ComputeHash($fs)) -replace '-', '').ToLower() }
        finally { $fs.Dispose(); $sha.Dispose() }
    }

    # ---- Ed25519 verification (RFC 8032, section 5.1.7) ----
    function Test-JazzEd25519([byte[]]$PublicKey, [byte[]]$Message, [byte[]]$Signature) {
        if ($PublicKey.Length -ne 32 -or $Signature.Length -ne 64) { return $false }
        $BI = [System.Numerics.BigInteger]
        $zero = [System.Numerics.BigInteger]::Zero
        $one = [System.Numerics.BigInteger]::One
        $two = [System.Numerics.BigInteger]2
        $prime = $BI::Subtract($BI::Pow($two, 255), [System.Numerics.BigInteger]19)
        $L = $BI::Add($BI::Pow($two, 252), $BI::Parse('27742317777372353535851937790883648493'))
        function M([System.Numerics.BigInteger]$a) {
            $r = $BI::Remainder($a, $prime)
            if ($r.Sign -lt 0) { $r = $BI::Add($r, $prime) }
            $r
        }
        function Inv([System.Numerics.BigInteger]$a) { $BI::ModPow((M $a), $BI::Subtract($prime, $two), $prime) }
        $d = M ($BI::Multiply([System.Numerics.BigInteger](-121665), (Inv ([System.Numerics.BigInteger]121666))))
        $d2 = M ($BI::Multiply($d, $two))
        $sqrtM1 = $BI::ModPow($two, $BI::Divide($BI::Subtract($prime, $one), [System.Numerics.BigInteger]4), $prime)
        function FromLE([byte[]]$b) { [System.Numerics.BigInteger]::new([byte[]]($b + [byte]0)) }
        # extended coordinates (X, Y, Z, T); the unified addition also doubles
        function PAdd($P, $Q) {
            $A = M ($BI::Multiply($BI::Subtract($P[1], $P[0]), $BI::Subtract($Q[1], $Q[0])))
            $B = M ($BI::Multiply($BI::Add($P[1], $P[0]), $BI::Add($Q[1], $Q[0])))
            $C = M ($BI::Multiply($BI::Multiply($P[3], $d2), $Q[3]))
            $D = M ($BI::Multiply($BI::Multiply($P[2], $two), $Q[2]))
            $E = $BI::Subtract($B, $A); $F = $BI::Subtract($D, $C); $G = $BI::Add($D, $C); $H = $BI::Add($B, $A)
            , @((M ($BI::Multiply($E, $F))), (M ($BI::Multiply($G, $H))), (M ($BI::Multiply($F, $G))), (M ($BI::Multiply($E, $H))))
        }
        function PMul([System.Numerics.BigInteger]$s, $P) {
            $Q = @($zero, $one, $one, $zero)
            while ($s.Sign -gt 0) {
                if (-not $s.IsEven) { $Q = PAdd $Q $P }
                $P = PAdd $P $P
                $s = $BI::Divide($s, $two)
            }
            , $Q
        }
        function PDecode([byte[]]$b) {
            $bb = [byte[]]$b.Clone()
            $x0 = ($bb[31] -shr 7) -band 1
            $bb[31] = $bb[31] -band 0x7f
            $y = FromLE $bb
            if ($y.CompareTo($prime) -ge 0) { return $null }
            $yy = M ($BI::Multiply($y, $y))
            $u = M ($BI::Subtract($yy, $one))
            $v = M ($BI::Add($BI::Multiply($d, $yy), $one))
            $v3 = $BI::ModPow($v, [System.Numerics.BigInteger]3, $prime)
            $v7 = $BI::ModPow($v, [System.Numerics.BigInteger]7, $prime)
            $e = $BI::Divide($BI::Subtract($prime, [System.Numerics.BigInteger]5), [System.Numerics.BigInteger]8)
            $x = M ($BI::Multiply($BI::Multiply($u, $v3), $BI::ModPow((M ($BI::Multiply($u, $v7))), $e, $prime)))
            $vxx = M ($BI::Multiply($v, $BI::Multiply($x, $x)))
            if (-not $vxx.Equals($u)) {
                if ($vxx.Equals((M ($BI::Negate($u))))) { $x = M ($BI::Multiply($x, $sqrtM1)) } else { return $null }
            }
            if ($x.IsZero -and $x0 -eq 1) { return $null }
            if ([int]($x.IsEven -eq $false) -ne $x0) { $x = $BI::Subtract($prime, $x) }
            , @($x, $y, $one, (M ($BI::Multiply($x, $y))))
        }
        function PEncode($P) {
            $zi = Inv $P[2]
            $x = M ($BI::Multiply($P[0], $zi))
            $y = M ($BI::Multiply($P[1], $zi))
            $raw = $y.ToByteArray()
            $out = New-Object byte[] 32
            [Array]::Copy($raw, $out, [Math]::Min($raw.Length, 32))
            if (-not $x.IsEven) { $out[31] = [byte]($out[31] -bor 0x80) }
            [Convert]::ToBase64String($out)
        }
        $Bx = $BI::Parse('15112221349535400772501151409588531511454012693041857206046113283949847762202')
        $By = $BI::Parse('46316835694926478169428394003475163141307993866256225615783033603165251855960')
        $Bpt = @($Bx, $By, $one, (M ($BI::Multiply($Bx, $By))))

        $Apt = PDecode $PublicKey
        if ($null -eq $Apt) { return $false }
        $Rb = [byte[]]$Signature[0..31]
        $S = FromLE ([byte[]]$Signature[32..63])
        if ($S.CompareTo($L) -ge 0) { return $false }
        $Rpt = PDecode $Rb
        if ($null -eq $Rpt) { return $false }
        $sha = [System.Security.Cryptography.SHA512]::Create()
        $h = $sha.ComputeHash([byte[]]($Rb + $PublicKey + $Message))
        $k = $BI::Remainder((FromLE $h), $L)
        $lhs = PEncode (PMul $S $Bpt)
        $rhs = PEncode (PAdd $Rpt (PMul $k $Apt))
        return ($lhs -ceq $rhs)
    }

    $repo = 'jazzautomations/jazz-cli'
    $arch = $env:JAZZ_ARCH
    if (-not $arch) {
        $pa = $env:PROCESSOR_ARCHITEW6432
        if (-not $pa) { $pa = $env:PROCESSOR_ARCHITECTURE }
        switch -Regex ($pa) {
            '^(AMD64|x64)$' { $arch = 'amd64' }
            '^ARM64$' { $arch = 'arm64' }
            default { Fail "arquitetura não suportada: $pa (há binários para amd64 e arm64)" }
        }
    }
    if ($arch -notin @('amd64', 'arm64')) { Fail "arquitetura não suportada: $arch" }
    $asset = "jazz-windows-$arch.exe"
    $base = if ($env:JAZZ_BASE_URL) { $env:JAZZ_BASE_URL.TrimEnd('/') } else { "https://github.com/$repo/releases/latest/download" }
    $binDir = if ($env:JAZZ_BIN_DIR) { $env:JAZZ_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\jazz' }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("jazz-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "› baixando $asset"
        $exeTmp = Join-Path $tmp 'jazz.exe'
        $man = Join-Path $tmp 'manifest.txt'
        $sig = Join-Path $tmp 'manifest.txt.sig'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $exeTmp
        Invoke-WebRequest -UseBasicParsing -Uri "$base/manifest.txt" -OutFile $man
        Invoke-WebRequest -UseBasicParsing -Uri "$base/manifest.txt.sig" -OutFile $sig

        $manBytes = [IO.File]::ReadAllBytes($man)
        if ($JazzPubKey) {
            try {
                $pk = [Convert]::FromBase64String($JazzPubKey.Trim())
                $sb = [Convert]::FromBase64String(([IO.File]::ReadAllText($sig)).Trim())
            } catch { Fail 'assinatura da release ilegível: instalação abortada' }
            if (-not (Test-JazzEd25519 $pk $manBytes $sb)) { Fail 'assinatura da release inválida: instalação abortada' }
            Write-Host '› assinatura ok (ed25519)'
        } elseif ($env:JAZZ_REQUIRE_SIG -eq '1') {
            Fail 'este instalador não tem chave pública e JAZZ_REQUIRE_SIG=1'
        } else {
            Write-Warning 'jazz: assinatura NÃO verificada (instalador sem chave); confiro só o sha256'
        }

        $lines = ([Text.Encoding]::UTF8.GetString($manBytes)) -split "`r?`n"
        if ($lines[0].Trim() -ne 'jazz-cli release manifest v1') { Fail 'manifest.txt inválido' }
        $want = $null
        foreach ($l in $lines) {
            $f = -split $l
            if ($f.Count -ge 2 -and ($f[1] -eq $asset -or $f[1] -eq "*$asset")) { $want = $f[0].ToLower() }
        }
        if (-not $want) { Fail "o manifesto não lista $asset" }
        $got = Get-Sha256Hex $exeTmp
        if ($got -ne $want) { Fail "sha256 não confere (esperado $want, recebido $got)" }

        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        $dest = Join-Path $binDir 'jazz.exe'
        if (Test-Path -LiteralPath $dest) {
            # a running jazz.exe cannot be overwritten, but it can be renamed
            Remove-Item -LiteralPath "$dest.old" -Force -ErrorAction SilentlyContinue
            Move-Item -LiteralPath $dest -Destination "$dest.old" -Force
        }
        Move-Item -LiteralPath $exeTmp -Destination $dest -Force
        Write-Host "✓ jazz instalado em $dest"

        if ($env:JAZZ_NO_PATH -ne '1') {
            $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
            $parts = @()
            if ($userPath) { $parts = $userPath -split ';' | Where-Object { $_ } }
            if ($parts -notcontains $binDir) {
                [Environment]::SetEnvironmentVariable('Path', (($parts + $binDir) -join ';'), 'User')
                $env:Path = "$env:Path;$binDir"
                Write-Host "  $binDir foi adicionado ao PATH do usuário (abra um terminal novo nos outros consoles)"
            }
        }
        try { & $dest version } catch {}
        Write-Host '  próximo passo: jazz key openrouter   (ou jazz login, jazz omni setup)'
        Write-Host '  dica: instale o Git for Windows (Git Bash): com ele o jazz usa bash; sem ele, PowerShell.'
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}
