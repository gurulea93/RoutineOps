# Генерация самоподписанного сертификата для подписи Windows-бинарей (Authenticode).
# Запускать на Windows-машине от администратора.
#
# Usage:
#   .\gen-win-codesign.ps1                    # генерирует сертификат + экспортирует .pfx и .cer
#   .\gen-win-codesign.ps1 -Password "p@ss"   # с явным паролем

param(
    [string]$Subject = "CN=RoutineOps",
    [string]$Password = "",
    [string]$OutDir = "C:\mdm-codesign"
)

$ErrorActionPreference = "Stop"

if ($Password -eq "") {
    $raw = -join ((65..90) + (97..122) + (48..57) | Get-Random -Count 20 | ForEach-Object { [char]$_ })
    $Password = $raw
    Write-Host "Generated password: $Password"
    Write-Host "Save it — needed for signing."
}

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$cert = New-SelfSignedCertificate `
    -Type CodeSigningCert `
    -Subject $Subject `
    -KeyAlgorithm RSA `
    -KeyLength 4096 `
    -HashAlgorithm SHA256 `
    -CertStoreLocation "Cert:\LocalMachine\My" `
    -NotAfter (Get-Date).AddYears(5)

$pfxPath = "$OutDir\mdm-codesign.pfx"
$cerPath = "$OutDir\mdm-codesign.cer"

$pwd = ConvertTo-SecureString -String $Password -Force -AsPlainText
Export-PfxCertificate -Cert $cert -FilePath $pfxPath -Password $pwd | Out-Null
Export-Certificate -Cert $cert -FilePath $cerPath -Type CERT | Out-Null

Write-Host ""
Write-Host "Certificate generated:"
Write-Host "  PFX (for signing): $pfxPath"
Write-Host "  CER (for deploy):  $cerPath"
Write-Host ""
Write-Host "Deploy CER to managed machines:"
Write-Host "  Import-Certificate -FilePath '$cerPath' -CertStoreLocation Cert:\LocalMachine\TrustedPublisher"
Write-Host "  Import-Certificate -FilePath '$cerPath' -CertStoreLocation Cert:\LocalMachine\Root"
Write-Host ""
Write-Host "Sign binary:"
Write-Host "  signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /f '$pfxPath' /p '$Password' agent.exe"
