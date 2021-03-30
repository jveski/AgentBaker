# Define all exit codes in Windows CSE

function Set-ExitCode
{
    Param(
        [Parameter(Mandatory=$true)][int]
        $ExitCode,
        [Parameter(Mandatory=$true)][string]
        $ErrorMessage,
    )
    Write-Log "Set ExitCode to $ExitCode and exit"
    # Construct CSE standard response for AKS
    # { "ExitCode": "0", "Output": "Started Kubelet.", "Error": "", "ExecDuration": "252799" }
    
    $JSON_STRING=[string]::Format('{{ExitCode: {0}, Output: {1}, Error: {2}, ExecDuration: {3}}}', $ExitCode, $OUTPUT, $ErrorMessage, $EXECUTION_DURATION)
    echo $JSON_STRING
    exit $ExitCode
}

$global:WINDOWS_UNEXPECTED_CSE_EXIT_CODE=1 # For unexpected error caught by the catch block in kuberneteswindowssetup.ps1