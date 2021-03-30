[CmdletBinding(DefaultParameterSetName="Standard")]
param(
    [parameter(Mandatory=$true)]
    [ValidateNotNullOrEmpty()]
    $PSFile,

    [parameter(Mandatory=$true)]
    [ValidateNotNullOrEmpty()]
    $LogFile,

    [parameter(Mandatory=$true)]
    [ValidateNotNullOrEmpty()]
    $AgentKey,

    [parameter(Mandatory=$true)]
    [ValidateNotNullOrEmpty()]
    $AADClientId,

    [parameter(Mandatory=$true)]
    [ValidateNotNullOrEmpty()]
    $AADClientSecret
)
# Do not parse the start time from $LogFile to simplify the logic
$StartTime=Get-Date
Invoke-Expression('{0} -AgentKey {1} -AADClientId {2} -AADClientSecret {3}' -f $PSFile, $AgentKey, $AADClientId, $AADClientSecret  | Out-File $LogFile -Encoding UTF8
$ExitCode=$LASTEXITCODE
$Output=$(Get-Content $LogFile | Select -Last 5) -join " | "
$Output=$Output -replace "`"", "'"
$ExecutionDuration=$(New-Timespan –Start $StartTime –End $(Get-Date))

$JsonString = '{{ExitCode: "{0}", Output: "{1}", Error: "{2}", ExecDuration: "{3}"}}' -f $ExitCode, $Output, "", $ExecutionDuration.TotalMilliseconds
echo $JsonString
exit $ExitCode