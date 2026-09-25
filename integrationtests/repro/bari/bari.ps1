<#
.SYNOPSIS
Mimics the parts of bari that collide with a C# language server.

.DESCRIPTION
Reproduces the layout bari generates for the QVI EVOLVE Suite:
- SDK-style project files generated inside the source tree
  (src/<Module>/<Project>/cs/<Project>.csproj), rewritten on every build;
- unconditional BaseIntermediateOutputPath/IntermediateOutputPath under
  target/tmp/<Module>/<Project>, and a synthetic Bari configuration/platform;
- solutions under target/, the one with tests having more projects;
- clean deletes target/ and then every generated project file with a plain
  File.Delete, failing with exit code 2 on the first error like bari's
  CsprojCleaner.

WPF projects use XAML that references local types, so the markup compiler
generates <Project>_<random>_wpftmp.csproj next to the real project.

Commands: generate (sources only), clean, build, rebuild, and designtime,
which runs Roslyn-style design-time builds in a loop (see Invoke-DesignTimeLoop).
#>
param(
    [Parameter(Mandatory = $true)][ValidateSet('generate', 'clean', 'build', 'rebuild', 'designtime')][string]$Command,
    [Parameter(Mandatory = $true)][string]$Workspace,
    [int]$Modules = 6,
    [int]$PagesPerModule = 10
)

$ErrorActionPreference = 'Stop'
$Workspace = [IO.Path]::GetFullPath($Workspace)
$Target = Join-Path $Workspace 'target'
$Utf8 = New-Object System.Text.UTF8Encoding($false)

function Write-Text([string]$Path, [string]$Text) {
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path)) | Out-Null
    [IO.File]::WriteAllText($Path, $Text, $Utf8)
}

function Get-Projects {
    $projects = @([pscustomobject]@{ Module = 'Core'; Name = 'Core.Model'; Kind = 'lib'; Refs = @(); Tests = $false })
    for ($m = 1; $m -le $Modules; $m++) {
        $refs = @('Core.Model')
        if ($m -gt 1) { $refs += "Extensions.Mod$($m - 1)" }
        $projects += [pscustomobject]@{ Module = "Mod$m"; Name = "Extensions.Mod$m"; Kind = 'wpf'; Refs = $refs; Tests = $false }
    }
    $projects += [pscustomobject]@{ Module = 'Shell'; Name = 'Suite.Shell'; Kind = 'app'; Refs = @('Core.Model', "Extensions.Mod$Modules"); Tests = $false }
    $projects += [pscustomobject]@{ Module = 'Core'; Name = 'Core.Model.Tests'; Kind = 'lib'; Refs = @('Core.Model'); Tests = $true }
    return $projects
}

function Get-ProjectDir($p) { Join-Path $Workspace "src\$($p.Module)\$($p.Name)\cs" }
function Get-ProjectFile($p) { Join-Path (Get-ProjectDir $p) "$($p.Name).csproj" }

# ---------------------------------------------------------------- sources

function New-Sources {
    foreach ($p in Get-Projects) {
        $dir = Get-ProjectDir $p
        if (Test-Path (Join-Path $dir 'Sources.done')) { continue }
        $ns = $p.Name
        switch ($p.Kind) {
            'lib' {
                if ($p.Tests) {
                    Write-Text (Join-Path $dir 'WidgetCatalogTests.cs') @"
namespace $ns;

public static class WidgetCatalogTests
{
    public static bool LooksUpKnownWidget() => new Core.Model.WidgetCatalog().Find("gauge") is not null;
}
"@
                } else {
                    Write-Text (Join-Path $dir 'WidgetCatalog.cs') @"
using System.Collections.Generic;

namespace Core.Model;

public sealed class Widget
{
    public Widget(string name) => Name = name;
    public string Name { get; }
}

public sealed class WidgetCatalog
{
    private readonly Dictionary<string, Widget> widgets = new();

    public Widget? Find(string name) => widgets.TryGetValue(name, out var widget) ? widget : null;

    public Widget Register(string name) => widgets[name] = new Widget(name);
}
"@
                }
            }
            'wpf' {
                Write-Text (Join-Path $dir 'UI\ModuleConverter.cs') @"
using System;
using System.Globalization;
using System.Windows.Data;

namespace $ns.UI;

public sealed class ModuleConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture) => value?.ToString() ?? string.Empty;
    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture) => value;
}
"@
                for ($i = 1; $i -le $PagesPerModule; $i++) {
                    $sub = if ($i % 2) { 'Controls' } else { 'Dialogs' }
                    Write-Text (Join-Path $dir "UI\$sub\Panel$i.xaml") @"
<UserControl x:Class="$ns.UI.$sub.Panel$i"
             xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
             xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"
             xmlns:local="clr-namespace:$ns.UI">
    <UserControl.Resources>
        <local:ModuleConverter x:Key="Converter" />
    </UserControl.Resources>
    <StackPanel>
        <TextBlock x:Name="Title" Text="{Binding Name, Converter={StaticResource Converter}}" />
        <Button x:Name="Apply" Content="Apply $i" Click="OnApply" />
    </StackPanel>
</UserControl>
"@
                    Write-Text (Join-Path $dir "UI\$sub\Panel$i.xaml.cs") @"
using System.Windows;
using System.Windows.Controls;

namespace $ns.UI.$sub;

public partial class Panel$i : UserControl
{
    private readonly Core.Model.WidgetCatalog catalog = new();

    public Panel$i()
    {
        InitializeComponent();
        DataContext = catalog.Register("panel$i");
    }

    private void OnApply(object sender, RoutedEventArgs e) => Title.Text = catalog.Find("panel$i")?.Name;
}
"@
                }
            }
            'app' {
                Write-Text (Join-Path $dir 'App.xaml') @"
<Application x:Class="Suite.Shell.App"
             xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
             xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"
             StartupUri="MainWindow.xaml" />
"@
                Write-Text (Join-Path $dir 'App.xaml.cs') @"
using System.Windows;

namespace Suite.Shell;

public partial class App : Application
{
}
"@
                Write-Text (Join-Path $dir 'MainWindow.xaml') @"
<Window x:Class="Suite.Shell.MainWindow"
        xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
        xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"
        xmlns:ext="clr-namespace:Extensions.Mod$Modules.UI.Controls;assembly=Extensions.Mod$Modules"
        Title="Shell">
    <ext:Panel1 />
</Window>
"@
                Write-Text (Join-Path $dir 'MainWindow.xaml.cs') @"
using System.Windows;

namespace Suite.Shell;

public partial class MainWindow : Window
{
    private readonly Core.Model.WidgetCatalog catalog = new();

    public MainWindow() => InitializeComponent();
}
"@
            }
        }
        Write-Text (Join-Path $dir 'Sources.done') 'generated'
    }
}

# ---------------------------------------------------------------- projects

# Like bari's DefaultProjectGuidManagement: GUIDs live in cache\guids, which a
# clean deletes, so every clean gives every project a new ProjectGuid.
function Get-ProjectGuid($p) {
    $file = Join-Path $Workspace 'cache\guids'
    $guids = @{}
    if (Test-Path $file) {
        foreach ($line in Get-Content $file) { $name, $value = $line -split '=', 2; $guids[$name] = $value }
    }
    if (-not $guids.ContainsKey($p.Name)) {
        $guids[$p.Name] = [guid]::NewGuid().ToString('B')
        New-Item -ItemType Directory -Force (Split-Path $file) | Out-Null
        Set-Content $file ($guids.GetEnumerator() | ForEach-Object { "$($_.Key)=$($_.Value)" })
    }
    return $guids[$p.Name]
}

function New-ProjectXml($p) {
    $dir = Get-ProjectDir $p
    $root = Join-Path $Workspace ''
    $rel = '..\..\..\..'
    $items = New-Object System.Text.StringBuilder
    Get-ChildItem $dir -Recurse -File | Where-Object { $_.Name -notlike '*_wpftmp.csproj' } | Sort-Object FullName | ForEach-Object {
        $relPath = $_.FullName.Substring($dir.Length + 1)
        switch -Wildcard ($_.Name) {
            'App.xaml' { [void]$items.AppendLine("    <ApplicationDefinition Include=`"$relPath`" />"); break }
            '*.xaml' { [void]$items.AppendLine("    <Page Include=`"$relPath`" />"); break }
            '*.cs' { [void]$items.AppendLine("    <Compile Include=`"$relPath`" />"); break }
        }
    }
    $refs = New-Object System.Text.StringBuilder
    foreach ($name in $p.Refs) {
        $ref = Get-Projects | Where-Object Name -eq $name
        $refPath = "$rel\src\$($ref.Module)\$($ref.Name)\cs\$($ref.Name).csproj"
        [void]$refs.AppendLine("    <ProjectReference Include=`"$refPath`" />")
    }
    $wpf = if ($p.Kind -eq 'lib') { 'false' } else { 'true' }
    $outputType = if ($p.Kind -eq 'app') { 'WinExe' } else { 'Library' }
    $projectGuid = Get-ProjectGuid $p
    return @"
<Project TreatAsLocalProperty="SelfContained">
  <PropertyGroup>
    <BaseIntermediateOutputPath>${root}target\tmp\$($p.Module)\$($p.Name)\obj\</BaseIntermediateOutputPath>
  </PropertyGroup>
  <Import Project="Sdk.props" Sdk="Microsoft.NET.Sdk" />
  <PropertyGroup>
    <ProjectGuid>$projectGuid</ProjectGuid>
    <OutputPath>$rel\target\$($p.Module)</OutputPath>
    <IntermediateOutputPath>$rel\target\tmp\$($p.Module)\$($p.Name)</IntermediateOutputPath>
    <AppendTargetFrameworkToOutputPath>false</AppendTargetFrameworkToOutputPath>
    <AppendRuntimeIdentifierToOutputPath>false</AppendRuntimeIdentifierToOutputPath>
    <Configurations>Bari</Configurations>
    <Platforms>Bari</Platforms>
    <PlatformTarget>x64</PlatformTarget>
    <RuntimeIdentifier>win-x64</RuntimeIdentifier>
    <SelfContained>false</SelfContained>
    <UseWPF>$wpf</UseWPF>
    <UseWindowsForms>$wpf</UseWindowsForms>
    <TargetFramework>net10.0-windows</TargetFramework>
    <OutputType>$outputType</OutputType>
    <AssemblyName>$($p.Name)</AssemblyName>
    <RootNamespace>$($p.Name)</RootNamespace>
    <Nullable>enable</Nullable>
    <EnableDefaultItems>false</EnableDefaultItems>
    <GenerateAssemblyInfo>true</GenerateAssemblyInfo>
  </PropertyGroup>
  <ItemGroup>
$($items.ToString().TrimEnd())
  </ItemGroup>
  <ItemGroup>
$($refs.ToString().TrimEnd())
  </ItemGroup>
  <Import Project="Sdk.targets" Sdk="Microsoft.NET.Sdk" />
</Project>
"@
}

function New-SolutionText($projects) {
    $csharpSdk = '{9A19103F-16F7-4668-BE54-9A1E7A4F7556}'
    $sb = New-Object System.Text.StringBuilder
    [void]$sb.AppendLine('')
    [void]$sb.AppendLine('Microsoft Visual Studio Solution File, Format Version 12.00')
    [void]$sb.AppendLine('# Visual Studio Version 17')
    $ids = @{}
    foreach ($p in $projects) {
        $md5 = [Security.Cryptography.MD5]::Create()
        $ids[$p.Name] = ([guid]::new($md5.ComputeHash([Text.Encoding]::UTF8.GetBytes($p.Name)))).ToString('B').ToUpperInvariant()
        $rel = "..\src\$($p.Module)\$($p.Name)\cs\$($p.Name).csproj"
        [void]$sb.AppendLine("Project(`"$csharpSdk`") = `"$($p.Name)`", `"$rel`", `"$($ids[$p.Name])`"")
        [void]$sb.AppendLine('EndProject')
    }
    [void]$sb.AppendLine('Global')
    [void]$sb.AppendLine("`tGlobalSection(SolutionConfigurationPlatforms) = preSolution")
    [void]$sb.AppendLine("`t`tBari|Bari = Bari|Bari")
    [void]$sb.AppendLine("`tEndGlobalSection")
    [void]$sb.AppendLine("`tGlobalSection(ProjectConfigurationPlatforms) = postSolution")
    foreach ($p in $projects) {
        [void]$sb.AppendLine("`t`t$($ids[$p.Name]).Bari|Bari.ActiveCfg = Bari|Bari")
        [void]$sb.AppendLine("`t`t$($ids[$p.Name]).Bari|Bari.Build.0 = Bari|Bari")
    }
    [void]$sb.AppendLine("`tEndGlobalSection")
    [void]$sb.AppendLine('EndGlobal')
    return $sb.ToString()
}

# ---------------------------------------------------------------- diagnostics

$restartManager = @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;

public static class FileHolders
{
    [StructLayout(LayoutKind.Sequential)]
    struct RM_UNIQUE_PROCESS { public int dwProcessId; public System.Runtime.InteropServices.ComTypes.FILETIME ProcessStartTime; }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    struct RM_PROCESS_INFO
    {
        public RM_UNIQUE_PROCESS Process;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 256)] public string strAppName;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 64)] public string strServiceShortName;
        public int ApplicationType;
        public uint AppStatus;
        public uint TSSessionId;
        [MarshalAs(UnmanagedType.Bool)] public bool bRestartable;
    }

    [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)] static extern int RmStartSession(out uint handle, int flags, string key);
    [DllImport("rstrtmgr.dll")] static extern int RmEndSession(uint handle);
    [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)] static extern int RmRegisterResources(uint handle, uint nFiles, string[] files, uint nApps, IntPtr apps, uint nServices, string[] services);
    [DllImport("rstrtmgr.dll")] static extern int RmGetList(uint handle, out uint needed, ref uint count, [In, Out] RM_PROCESS_INFO[] infos, ref uint reasons);

    public static int[] Get(string[] files)
    {
        uint handle;
        if (RmStartSession(out handle, 0, Guid.NewGuid().ToString("N")) != 0) return new int[0];
        try
        {
            if (RmRegisterResources(handle, (uint)files.Length, files, 0, IntPtr.Zero, 0, null) != 0) return new int[0];
            uint needed, count = 0, reasons = 0;
            int rc = RmGetList(handle, out needed, ref count, null, ref reasons);
            if (rc != 234 || needed == 0) return new int[0];
            var infos = new RM_PROCESS_INFO[needed];
            count = needed;
            if (RmGetList(handle, out needed, ref count, infos, ref reasons) != 0) return new int[0];
            var pids = new List<int>();
            for (int i = 0; i < count; i++) pids.Add(infos[i].Process.dwProcessId);
            return pids.ToArray();
        }
        finally { RmEndSession(handle); }
    }
}
'@

function Show-Holders([string[]]$Files) {
    if (-not $Files) { return }
    if (-not ('FileHolders' -as [type])) { Add-Type -TypeDefinition $restartManager }
    $pids = [FileHolders]::Get($Files)
    if (-not $pids) {
        Write-Host "REPRO-HOLDERS none reported by Restart Manager for $($Files.Count) file(s)"
        return
    }
    foreach ($id in $pids) {
        $proc = Get-CimInstance Win32_Process -Filter "ProcessId=$id" -ErrorAction SilentlyContinue
        $cmd = if ($proc) { $proc.CommandLine } else { '<exited>' }
        $parent = if ($proc) { $proc.ParentProcessId } else { '?' }
        Write-Host "REPRO-HOLDERS pid=$id parent=$parent cmd=$cmd"
    }
}

# ---------------------------------------------------------------- commands

function Invoke-Clean {
    if (Test-Path $Target) {
        try {
            [IO.Directory]::Delete($Target, $true)
        } catch {
            $ex = if ($_.Exception.InnerException) { $_.Exception.InnerException } else { $_.Exception }
            Write-Host "Warning: Failed to clean target root: $($ex.Message)"
            $left = @(Get-ChildItem $Target -Recurse -File -ErrorAction SilentlyContinue | Select-Object -First 50 | ForEach-Object FullName)
            Write-Host "REPRO-TARGET-LEFT $($left.Count) file(s): $(($left | Select-Object -First 5) -join '; ')"
            Show-Holders $left
        }
    }
    Remove-Item (Join-Path $Workspace 'cache') -Recurse -Force -ErrorAction SilentlyContinue
    foreach ($p in Get-Projects) {
        $file = Get-ProjectFile $p
        if (-not (Test-Path $file)) { continue }
        Write-Host 'DEBUG - Deleting csproj file'
        try {
            [IO.File]::Delete($file)
        } catch {
            $ex = if ($_.Exception.InnerException) { $_.Exception.InnerException } else { $_.Exception }
            Write-Host $ex.ToString()
            Show-Holders @($file)
            Write-Host 'EXIT=2'
            exit 2
        }
    }
}

function Invoke-Build {
    $projects = Get-Projects
    foreach ($p in $projects) { Write-Text (Get-ProjectFile $p) (New-ProjectXml $p) }
    Write-Text (Join-Path $Target 'repro.sln') (New-SolutionText ($projects | Where-Object { -not $_.Tests }))
    Write-Text (Join-Path $Target 'repro-withtests.sln') (New-SolutionText $projects)
    $sln = Join-Path $Target 'repro-withtests.sln'
    & dotnet restore $sln -nologo -v:q -p:Configuration=Bari -p:Platform=Bari
    if ($LASTEXITCODE -ne 0) { Write-Host "EXIT=$LASTEXITCODE"; exit $LASTEXITCODE }
    & dotnet build $sln --no-restore -nologo -v:m -clp:NoSummary -p:Configuration=Bari -p:Platform=Bari
    if ($LASTEXITCODE -ne 0) { Write-Host "EXIT=$LASTEXITCODE"; exit $LASTEXITCODE }
}

# Runs design-time builds the way Roslyn's MSBuildWorkspace build host does on
# .NET (the global properties of its ProjectBuildManager, the targets it
# builds, and csharp-ls's TargetFramework) over the WPF projects until
# designtime.stop appears in the workspace. The environment, e.g.
# CustomBeforeMicrosoftCommonTargets, applies as it would to a language server.
function Invoke-DesignTimeLoop {
    $stop = Join-Path $Workspace 'designtime.stop'
    $properties = @(
        '-p:DesignTimeBuild=true', '-p:NonExistentFile=__NonExistentSubDir__\__NonExistentFile__',
        '-p:BuildProjectReferences=false', '-p:BuildingProject=false', '-p:ProvideCommandLineArgs=true',
        '-p:SkipCompilerExecution=true', '-p:ContinueOnError=ErrorAndContinue',
        '-p:ShouldUnsetParentConfigurationAndPlatform=false', '-p:TargetFramework=net10.0-windows')
    $builds = 0
    while (-not (Test-Path $stop)) {
        foreach ($p in Get-Projects | Where-Object Kind -ne 'lib') {
            if (Test-Path $stop) { break }
            $file = Get-ProjectFile $p
            if (-not (Test-Path $file)) { Start-Sleep -Milliseconds 200; continue }
            & dotnet msbuild $file -nologo -v:q -nodeReuse:false '-t:Compile;CoreCompile;DesignTimeMarkupCompilation' @properties | Out-Null
            $builds++
        }
    }
    Write-Host "REPRO-DESIGNTIME builds=$builds"
}

switch ($Command) {
    'generate' { New-Sources }
    'clean' { Invoke-Clean }
    'build' { New-Sources; Invoke-Build }
    'rebuild' { Invoke-Clean; New-Sources; Invoke-Build }
    'designtime' { Invoke-DesignTimeLoop }
}
Write-Host 'EXIT=0'
exit 0
