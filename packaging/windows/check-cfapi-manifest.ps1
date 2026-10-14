param([Parameter(Mandatory=$true)][string]$Helper)
$ErrorActionPreference = 'Stop'
if (-not ('FileesPeManifestReader' -as [type])) {
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
public static class FileesPeManifestReader {
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr LoadLibraryExW(string path, IntPtr file, uint flags);
    [DllImport("kernel32.dll", EntryPoint="FindResourceW", SetLastError=true)] static extern IntPtr FindResource(IntPtr module, IntPtr name, IntPtr type);
    [DllImport("kernel32.dll", SetLastError=true)] static extern uint SizeofResource(IntPtr module, IntPtr resource);
    [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr LoadResource(IntPtr module, IntPtr resource);
    [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr LockResource(IntPtr resource);
    [DllImport("kernel32.dll")] static extern bool FreeLibrary(IntPtr module);
    public static byte[] Read(string path) {
        // Resource/data mapping only: never run the candidate's entry point.
        IntPtr module=LoadLibraryExW(path,IntPtr.Zero,0x22);
        if(module==IntPtr.Zero) throw new Win32Exception();
        try {
            IntPtr resource=FindResource(module,(IntPtr)1,(IntPtr)24);
            if(resource==IntPtr.Zero) throw new InvalidOperationException("CFAPI helper has no process manifest");
            uint size=SizeofResource(module,resource);
            if(size==0 || size>1048576) throw new InvalidOperationException("Invalid CFAPI process manifest size");
            byte[] raw=new byte[size];
            Marshal.Copy(LockResource(LoadResource(module,resource)),raw,0,(int)size);
            return raw;
        } finally { FreeLibrary(module); }
    }
}
'@
}
$path=(Resolve-Path -LiteralPath $Helper).Path
$bytes=[FileesPeManifestReader]::Read($path)
$settings=[Xml.XmlReaderSettings]::new()
$settings.DtdProcessing=[Xml.DtdProcessing]::Prohibit
$settings.XmlResolver=$null
$reader=[Xml.XmlReader]::Create([IO.StringReader]::new([Text.Encoding]::UTF8.GetString($bytes).Trim([char]0)),$settings)
try {
    $xml=[Xml.XmlDocument]::new(); $xml.XmlResolver=$null; $xml.Load($reader)
} finally { $reader.Dispose() }
$names=[Xml.XmlNamespaceManager]::new($xml.NameTable)
$names.AddNamespace('a','urn:schemas-microsoft-com:asm.v1')
$names.AddNamespace('v3','urn:schemas-microsoft-com:asm.v3')
$names.AddNamespace('ws','http://schemas.microsoft.com/SMI/2016/WindowsSettings')
$flag=$xml.SelectSingleNode('/a:assembly/v3:application/v3:windowsSettings/ws:longPathAware',$names)
$level=$xml.SelectSingleNode('/a:assembly/v3:trustInfo/v3:security/v3:requestedPrivileges/v3:requestedExecutionLevel',$names)
if ($null -eq $flag -or $flag.InnerText.Trim() -cne 'true' -or $null -eq $level -or $level.GetAttribute('level') -cne 'asInvoker') {
    throw 'CFAPI helper must embed longPathAware=true and asInvoker; rebuild it from current sources'
}
Write-Output 'CFAPI process manifest: longPathAware=true, asInvoker'
