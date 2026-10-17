using System.Reflection;
using System.Diagnostics;
using DirectPlayback;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.MediaEncoding;
using MediaBrowser.Model.Dlna;
using MediaBrowser.Model.Dto;
using MediaBrowser.Model.Entities;
using MediaBrowser.Model.MediaInfo;
using Microsoft.Extensions.Logging.Abstractions;

public static class PolicyTests
{
    private static int _assertions;
    public static void Check(bool value, string message)
    {
        _assertions++;
        if (!value) throw new Exception(message);
    }

    public static List<MediaStream> Streams() =>
    [new() { Index = 0, Type = MediaStreamType.Video, Codec = "hevc", Width = 3840, Height = 2160 },
     new() { Index = 1, Type = MediaStreamType.Audio, Codec = "aac" },
     new() { Index = 2, Type = MediaStreamType.Audio }]; // unknown av3a companion track
    public static MediaInfo Info() => new() { MediaStreams = Streams(), RunTimeTicks = 123000000 };

    public static async Task Main()
    {
        var root = Path.Combine(Path.GetTempPath(), "direct-playback-tests-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            var options = new PolicyOptions { StrmRoots = [root], GatewayOrigins = ["http://direct:9096"], InitialTimeoutSeconds = 1, ExtendedTimeoutSeconds = 1 };
            var optionsPath = Path.Combine(root, "options.json");
            File.WriteAllText(optionsPath, System.Text.Json.JsonSerializer.Serialize(options));
            var statePath = Path.Combine(root, "state.json");
            var policy = new PlaybackPolicy(optionsPath, statePath, NullLogger<PlaybackPolicy>.Instance);
            var paths = DispatchProxy.Create<MediaBrowser.Common.Configuration.IApplicationPaths, FakePaths>();
            ((FakePaths)(object)paths).Root = root;
            var plugin = new Plugin(paths);
            Check(plugin.Version is not null && plugin.AssemblyFilePath.EndsWith(".dll"), "plugin assembly attributes initialized for loader");
            var path = Path.Combine(root, "episode.strm");
            File.WriteAllText(path, "http://direct:9096/direct/item?sig=test&jellyfin_sig=test");
            File.SetLastWriteTimeUtc(path, DateTime.UtcNow.AddMinutes(-2));
            var snapshot = policy.Snapshot(path)!;
            Check(snapshot is not null, "project STRM scope");
            Check(!policy.IsProjectUrl("http://other:9096/direct/item?sig=test"), "different origin untouched");
            Check(!policy.IsProjectUrl("http://direct:9096/other/item?sig=test"), "different path untouched");
            Check(!policy.IsProjectUrl("http://direct:9096/direct/item"), "unsigned URL untouched");
            Check(!policy.IsProjectUrl("http://user@direct:9096/direct/item?sig=test"), "userinfo untouched");
            Check(policy.Snapshot(Path.Combine(root, "..", "outside.strm")) is null, "outside root untouched");
            Check(PlaybackPolicy.Usable(Streams(), 123, false), "unknown companion codec keeps valid main streams usable");
            Check(!PlaybackPolicy.Usable(Streams(), null, false), "missing duration invalidates metadata");
            Check(!PlaybackPolicy.Usable([new() { Type = MediaStreamType.Video, Codec = "hevc" }], 123, false), "missing video dimensions invalidates metadata");
            var id = Guid.NewGuid();
            Check(!policy.CanReuse(id, snapshot!, DateTime.UtcNow.AddDays(-2), true), "expired bootstrap invalidated");
            Check(!policy.CanReuse(id, snapshot!, snapshot!.ModifiedUtc.AddMinutes(-1), true), "older metadata invalidated");
            Check(policy.CanReuse(id, snapshot!, DateTime.UtcNow, true), "fresh persisted metadata bootstraps");
            var bootstrap = DateTime.UtcNow.AddHours(-23);
            policy.Remember(id, snapshot!, bootstrap);
            var state = System.Text.Json.JsonSerializer.Deserialize<Dictionary<string, MetadataStamp>>(File.ReadAllText(statePath))!;
            Check(state[id.ToString("N")].RefreshedUtc == bootstrap, "reuse never extends freshness");
            File.AppendAllText(path, "&changed=1");
            var changed = policy.Snapshot(path)!;
            Check(changed.Fingerprint != snapshot!.Fingerprint, "URL change invalidates fingerprint");
            Check(!policy.CanReuse(id, changed, DateTime.UtcNow, true), "known source change forces refresh");
            var arguments = PlaybackPolicy.ProbeArguments("-analyzeduration 200M -probesize 1G -user_agent \"agent -probesize 1G\"", 5000000, 5);
            Check(arguments == "-analyzeduration 5000000 -probesize 5000000 -user_agent \"agent -probesize 1G\"", "probe bounds preserve quoted HTTP options");

            var inner = DispatchProxy.Create<IMediaEncoder, FakeEncoder>();
            var fake = (FakeEncoder)(object)inner;
            var encoder = ServiceProxy<IMediaEncoder>.Wrap(inner, policy);
            var request = new MediaInfoRequest { MediaSource = new() { Path = "http://direct:9096/direct/item?sig=test", Protocol = MediaProtocol.Http }, MediaType = DlnaProfileType.Video, ExtractChapters = true };
            fake.Probe = (_, _) => Task.FromResult(Info());
            await encoder.GetMediaInfo(request, CancellationToken.None);
            Check(fake.Arguments.Count == 1 && fake.Arguments[0].Contains("-probesize 5000000"), "usable small probe stops after one tier");
            Check(fake.Chapters, "chapter extraction preserved");
            fake.Arguments.Clear();
            fake.Probe = (_, _) => Task.FromResult(fake.Arguments.Count == 1 ? new MediaInfo() : Info());
            await encoder.GetMediaInfo(request, CancellationToken.None);
            Check(fake.Arguments.Count == 2 && fake.Arguments[1].Contains("-probesize 20000000"), "insufficient small probe falls back once");
            fake.Arguments.Clear();
            fake.Probe = (_, _) => fake.Arguments.Count == 1 ? throw new IOException("fixture") : Task.FromResult(Info());
            await encoder.GetMediaInfo(request, CancellationToken.None);
            Check(fake.Arguments.Count == 2, "failed small probe falls back once");
            fake.Arguments.Clear();
            fake.Probe = async (_, ct) => { if (fake.Arguments.Count == 1) await Task.Delay(5000, ct); return Info(); };
            await encoder.GetMediaInfo(request, CancellationToken.None);
            Check(fake.Arguments.Count == 2, "small timeout cancels before fallback");
            fake.Arguments.Clear();
            fake.SpawnProcess = true;
            using (var ct = new CancellationTokenSource(100))
            {
                fake.Probe = async (_, token) => { await Task.Delay(5000, token); return Info(); };
                try { await encoder.GetMediaInfo(request, ct.Token); throw new Exception("cancellation swallowed"); }
                catch (OperationCanceledException) { }
                Check(fake.LastProcess!.WaitForExit(1000), "cancellation kills exactly the started probe process");
                fake.LastProcess.Dispose();
            }
            fake.SpawnProcess = false;
            fake.Arguments.Clear();
            using (var ct = new CancellationTokenSource())
            {
                ct.Cancel();
                fake.Probe = (_, token) => Task.FromCanceled<MediaInfo>(token);
                try { await encoder.GetMediaInfo(request, ct.Token); throw new Exception("cancellation swallowed"); }
                catch (OperationCanceledException) { }
                Check(fake.Arguments.Count == 1, "caller cancellation never retries");
            }
            request.MediaSource.Path = "http://other/item";
            await encoder.GetMediaInfo(request, CancellationToken.None);
            Check(fake.OriginalCalls == 1, "other media uses original probe unchanged");

            var managerInner = DispatchProxy.Create<IMediaSourceManager, FakeSources>();
            var managerFake = (FakeSources)(object)managerInner;
            var manager = ServiceProxy<IMediaSourceManager>.Wrap(managerInner, policy);
            var item = new FixtureVideo { Id = Guid.NewGuid(), Path = path, RunTimeTicks = 123, DateLastRefreshed = DateTime.UtcNow.AddDays(-2) };
            managerFake.Resolve = async (probe) =>
            {
                if (probe) { await Task.Delay(50); item.DateLastRefreshed = DateTime.UtcNow; }
                return [Info()];
            };
            await Task.WhenAll(Enumerable.Range(0, 8).Select(_ => manager.GetPlaybackMediaSources(item, null!, true, true, CancellationToken.None)));
            Check(managerFake.Probes.Count(p => p) == 1 && managerFake.Probes.Count == 8, "concurrent requests coalesce refresh but each builds own sources");
            managerFake.Probes.Clear();
            managerFake.Resolve = probe => Task.FromResult<IReadOnlyList<MediaSourceInfo>>(probe ? [Info()] : [new MediaSourceInfo()]);
            await manager.GetPlaybackMediaSources(item, null!, true, false, CancellationToken.None);
            Check(managerFake.Probes.SequenceEqual(new[] { false, true }), "unexpected incomplete returned sources trigger one refresh");
            var refreshMarker = Path.Combine(root, "refresh", item.Id.ToString("N"));
            Directory.CreateDirectory(Path.GetDirectoryName(refreshMarker)!);
            File.WriteAllText(refreshMarker, "");
            managerFake.Probes.Clear();
            managerFake.Resolve = _ => Task.FromResult<IReadOnlyList<MediaSourceInfo>>([Info()]);
            await manager.GetPlaybackMediaSources(item, null!, true, false, CancellationToken.None);
            Check(managerFake.Probes.SequenceEqual(new[] { true }) && !File.Exists(refreshMarker), "one-shot force refresh probes and consumes marker");
            File.WriteAllText(optionsPath, "{broken");
            var invalid = new PlaybackPolicy(optionsPath, statePath, NullLogger<PlaybackPolicy>.Instance);
            Check(!invalid.Options.Enabled && !invalid.IsProjectUrl("http://direct:9096/direct/item?sig=test"), "bad configuration preserves Jellyfin's original behavior");
            Console.WriteLine($"PASS: {_assertions} assertions, scope/freshness/fallback/cancellation/concurrency");
        }
        finally { Directory.Delete(root, true); }
    }
}

public class FixtureVideo : Video
{
    public override IReadOnlyList<MediaStream> GetMediaStreams() => PolicyTests.Streams();
}

public class FakeEncoder : DispatchProxy
{
    private readonly System.Threading.Lock _runningProcessesLock = new();
    private readonly List<FakeProcessWrapper> _runningProcesses = new();
    public bool SpawnProcess;
    public Process? LastProcess;
    public Func<string, CancellationToken, Task<MediaInfo>> Probe = null!;
    public readonly List<string> Arguments = new();
    public bool Chapters;
    public int OriginalCalls;
    protected override object? Invoke(MethodInfo? method, object?[]? args) => method!.Name switch
    {
        "GetInputArgument" => "\"fixture\"",
        "GetMediaInfo" => Original(),
        _ => throw new NotSupportedException(method.Name)
    };
    private Task<MediaInfo> Original() { OriginalCalls++; return Task.FromResult(PolicyTests.Info()); }
    private string GetExtraArguments(MediaInfoRequest request) => "-analyzeduration 200M -probesize 1G -user_agent \"fixture\"";
    private Task<MediaInfo> GetMediaInfoInternal(string input, string path, MediaProtocol protocol, bool chapters, string extra, bool audio, VideoType? video, CancellationToken cancellation)
    {
        Arguments.Add(extra); Chapters = chapters;
        if (!SpawnProcess) return Probe(extra, cancellation);
        LastProcess = Process.Start(new ProcessStartInfo("sleep", "30") { UseShellExecute = false })!;
        var wrapper = new FakeProcessWrapper(LastProcess);
        lock (_runningProcessesLock) _runningProcesses.Add(wrapper);
        return Finish();
        async Task<MediaInfo> Finish()
        {
            try { return await Probe(extra, cancellation); }
            finally { lock (_runningProcessesLock) _runningProcesses.Remove(wrapper); }
        }
    }
    public sealed record FakeProcessWrapper(Process Process);
}

public class FakeSources : DispatchProxy
{
    public readonly List<bool> Probes = new();
    public Func<bool, Task<IReadOnlyList<MediaSourceInfo>>> Resolve = null!;
    protected override object? Invoke(MethodInfo? method, object?[]? args)
    {
        if (method!.Name != "GetPlaybackMediaSources") throw new NotSupportedException(method.Name);
        var probe = (bool)args![2]!; Probes.Add(probe); return Resolve(probe);
    }
}

public class FakePaths : DispatchProxy
{
    public string Root = "";
    protected override object? Invoke(MethodInfo? method, object?[]? args) => Root;
}
