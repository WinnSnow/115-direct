using System.Diagnostics;
using System.Collections;
using System.ComponentModel;
using System.Reflection;
using System.Runtime.ExceptionServices;
using System.Security.Cryptography;
using System.Text;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.MediaEncoding;
using MediaBrowser.Model.Dlna;
using MediaBrowser.Model.Dto;
using MediaBrowser.Model.MediaInfo;

namespace DirectPlayback;

public class ServiceProxy<T> : DispatchProxy, IDisposable where T : class
{
    private T _inner = null!;
    private PlaybackPolicy _policy = null!;

    public static T Wrap(T inner, PlaybackPolicy policy)
    {
        var result = Create<T, ServiceProxy<T>>();
        var proxy = (ServiceProxy<T>)(object)result;
        proxy._inner = inner;
        proxy._policy = policy;
        return result;
    }

    protected override object? Invoke(MethodInfo? method, object?[]? arguments)
    {
        ArgumentNullException.ThrowIfNull(method);
        arguments ??= [];
        if (typeof(T) == typeof(IMediaSourceManager) && method.Name == "GetPlaybackMediaSources"
            && arguments is [BaseItem item, _, true, _, CancellationToken])
        {
            var snapshot = _policy.Snapshot(item.Path);
            if (snapshot is not null) return PlaybackSources(method, arguments, item);
        }
        if (typeof(T) == typeof(IMediaEncoder) && method.Name == "GetMediaInfo"
            && arguments is [MediaInfoRequest request, CancellationToken cancellation]
            && _policy.IsProjectUrl(request.MediaSource.Path))
        {
            var original = FindMethod(_inner.GetType(), "GetMediaInfoInternal", 8);
            var extra = FindMethod(_inner.GetType(), "GetExtraArguments", 1);
            if (original is not null && extra is not null
                && FindField(_inner.GetType(), "_runningProcessesLock") is not null
                && FindField(_inner.GetType(), "_runningProcesses") is not null)
                return Probe(request, cancellation, original, extra);
            _policy.LogUnsupportedEncoder();
        }
        return Call(method, _inner, arguments);
    }

    private async Task<IReadOnlyList<MediaSourceInfo>> PlaybackSources(MethodInfo method, object?[] arguments, BaseItem item)
    {
        var watch = Stopwatch.StartNew();
        var cancellation = (CancellationToken)arguments[4]!;
        var gate = _policy.ItemLocks.GetOrAdd(item.Id, _ => new SemaphoreSlim(1, 1));
        await gate.WaitAsync(cancellation).ConfigureAwait(false);
        try
        {
            var snapshot = _policy.Snapshot(item.Path);
            var reusable = snapshot is not null && _policy.CanReuse(item.Id, snapshot, item.DateLastRefreshed,
                PlaybackPolicy.Usable(item.GetMediaStreams(), item.RunTimeTicks, item.MediaType.ToString() == "Audio"));
            var forwarded = (object?[])arguments.Clone();
            if (reusable) forwarded[2] = false;
            var result = await ((Task<IReadOnlyList<MediaSourceInfo>>)Call(method, _inner, forwarded)!).ConfigureAwait(false);
            // Recheck the returned sources, since a provider can return sources
            // other than the item's persisted primary source.
            if (reusable && (result.Count == 0 || !result.All(source => PlaybackPolicy.Usable(source.MediaStreams, source.RunTimeTicks, item.MediaType.ToString() == "Audio"))))
            {
                reusable = false;
                forwarded[2] = true;
                result = await ((Task<IReadOnlyList<MediaSourceInfo>>)Call(method, _inner, forwarded)!).ConfigureAwait(false);
            }
            var after = _policy.Snapshot(item.Path);
            if (snapshot is not null && after?.Fingerprint == snapshot.Fingerprint
                && result.Count > 0 && result.All(source => PlaybackPolicy.Usable(source.MediaStreams, source.RunTimeTicks, item.MediaType.ToString() == "Audio")))
            {
                _policy.Remember(item.Id, snapshot, reusable ? item.DateLastRefreshed : null);
            }
            _policy.LogMetadata(item.Id, reusable ? "reused" : "refreshed", watch.Elapsed.TotalMilliseconds);
            return result;
        }
        finally
        {
            gate.Release();
        }
    }

    private async Task<MediaInfo> Probe(MediaInfoRequest request, CancellationToken cancellation, MethodInfo original, MethodInfo extra)
    {
        var encoder = (IMediaEncoder)(object)_inner;
        var originalArguments = (string)Call(extra, _inner, [request])!;
        var sourceKey = Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(request.MediaSource.Path)))[..12];
        for (var tier = 0; tier < 2; tier++)
        {
            var extended = tier == 1;
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
            timeout.CancelAfter(TimeSpan.FromSeconds(extended ? _policy.Options.ExtendedTimeoutSeconds : _policy.Options.InitialTimeoutSeconds));
            var probeArguments = PlaybackPolicy.ProbeArguments(originalArguments,
                extended ? _policy.Options.ExtendedProbeBytes : _policy.Options.InitialProbeBytes,
                extended ? _policy.Options.ExtendedAnalyzeSeconds : _policy.Options.InitialAnalyzeSeconds);
            var watch = Stopwatch.StartNew();
            try
            {
                var (task, process) = StartTrackedProbe(original,
                    [encoder.GetInputArgument(request.MediaSource.Path, request.MediaSource), request.MediaSource.Path,
                     request.MediaSource.Protocol, request.ExtractChapters, probeArguments,
                     request.MediaType == DlnaProfileType.Audio, request.MediaSource.VideoType, timeout.Token]);
                using var cleanup = timeout.Token.Register(() =>
                {
                    // Process.Dispose alone does not stop ffprobe on this
                    // Jellyfin version. Kill only the process started by this
                    // invocation, including on a caller cancellation.
                    try { if (process is not null && !process.HasExited) process.Kill(entireProcessTree: true); }
                    catch (Exception error) when (error is InvalidOperationException or Win32Exception) { }
                });
                var result = await task.ConfigureAwait(false);
                var usable = PlaybackPolicy.Usable(result.MediaStreams, result.RunTimeTicks, request.MediaType == DlnaProfileType.Audio);
                _policy.LogProbe(sourceKey, extended ? "extended" : "small", watch.Elapsed.TotalMilliseconds, usable);
                if (usable) return result;
            }
            catch (OperationCanceledException) when (cancellation.IsCancellationRequested) { throw; }
            catch (Exception error) when (!extended && error is not OutOfMemoryException)
            {
                _policy.LogProbe(sourceKey, "small_failed", watch.Elapsed.TotalMilliseconds, false);
            }
            catch (Exception error) when (extended && error is not OutOfMemoryException)
            {
                _policy.LogProbe(sourceKey, "extended_failed", watch.Elapsed.TotalMilliseconds, false);
                throw;
            }
        }
        throw new InvalidOperationException("115 media probe did not produce usable media information within the bounded probe tiers");
    }

    private (Task<MediaInfo> Task, Process? Process) StartTrackedProbe(MethodInfo method, object?[] arguments)
    {
        var type = _inner.GetType();
        var guard = FindField(type, "_runningProcessesLock")?.GetValue(_inner)
            ?? throw new NotSupportedException("Jellyfin probe process lock is unavailable");
        var processes = FindField(type, "_runningProcesses")?.GetValue(_inner) as IEnumerable
            ?? throw new NotSupportedException("Jellyfin probe process registry is unavailable");
        // GetMediaInfoInternal starts the process synchronously before its first
        // await. Holding its existing registry lock isolates that exact start
        // from probes launched by other callers. .NET Lock and Monitor differ.
        if (guard is System.Threading.Lock nativeLock)
        {
            lock (nativeLock) return Start();
        }
        lock (guard) return Start();

        (Task<MediaInfo>, Process?) Start()
        {
            var before = processes.Cast<object>().ToHashSet(ReferenceEqualityComparer.Instance);
            var task = (Task<MediaInfo>)Call(method, _inner, arguments)!;
            var wrapper = processes.Cast<object>().FirstOrDefault(value => !before.Contains(value));
            var process = wrapper?.GetType().GetProperty("Process")?.GetValue(wrapper) as Process;
            return (task, process);
        }
    }

    private static FieldInfo? FindField(Type type, string name)
    {
        for (Type? current = type; current is not null; current = current.BaseType)
        {
            var field = current.GetField(name, BindingFlags.Instance | BindingFlags.NonPublic | BindingFlags.DeclaredOnly);
            if (field is not null) return field;
        }
        return null;
    }

    private static MethodInfo? FindMethod(Type type, string name, int parameters)
    {
        for (Type? current = type; current is not null; current = current.BaseType)
        {
            var method = current.GetMethods(BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.DeclaredOnly)
                .FirstOrDefault(m => m.Name == name && m.GetParameters().Length == parameters);
            if (method is not null) return method;
        }
        return null;
    }

    private static object? Call(MethodInfo method, object target, object?[] arguments)
    {
        try { return method.Invoke(target, arguments); }
        catch (TargetInvocationException error) when (error.InnerException is not null)
        {
            ExceptionDispatchInfo.Capture(error.InnerException).Throw();
            throw;
        }
    }

    public void Dispose()
    {
        if (_inner is IDisposable disposable) disposable.Dispose();
    }
}
