echo %DATE%,%TIME%,%COMPUTERNAME% > %SYSTEMDRIVE%\AzureData\CustomDataSetupScript.log 2>&1
powershell.exe -ExecutionPolicy Unrestricted -command \"
$arguments = '
-PSFile %SYSTEMDRIVE%\AzureData\windows\kuberneteswindowssetup.ps1
-LogFile %SYSTEMDRIVE%\AzureData\CustomDataSetupScript.log
-AgentKey {{ GetParameter "clientPrivateKey" }}
-AADClientId {{ GetParameter "servicePrincipalClientId" }}
-AADClientSecret ''{{ GetParameter "encodedServicePrincipalClientSecret" }}''';
$inputFile = '%SYSTEMDRIVE%\AzureData\CustomData.bin';
$outputFile = '%SYSTEMDRIVE%\AzureData\CustomDataSetupScript.ps1';
Copy-Item $inputFile $outputFile;
Invoke-Expression('{0} {1}' -f $outputFile, $arguments);
\"