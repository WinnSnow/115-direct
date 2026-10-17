using System.Collections.Concurrent;
using System.Diagnostics;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using MediaBrowser.Common.Configuration;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Model.Entities;
using Microsoft.Extensions.Logging;

namespace DirectPlayback;

public sealed class PolicyOptions
{
    public bool Enabled { get; set; } = true;
    public string[] StrmRoots { get; set; } = ["/media/115-strm"];
    public string[] GatewayOrigins { get; set; } = ["http://direct:9096"];
    public int InitialProbeBytes { get; set; } = 5_000_000;
    public int InitialAnalyzeSeconds { get; set; } = 5;
    public int InitialTimeoutSeconds { get; set; } = 12;
    public int ExtendedProbeBytes { get; set; } = 20_000_000;
    public int ExtendedAnalyzeSeconds { get; set; } = 10;
    public int ExtendedTimeoutSeconds { get; set; } = 20;
    public int MetadataMaxAgeHours { get; set; } = 24;

    public void Validate()
    {
        if (StrmRoots is null || StrmRoots.Length == 0 || StrmRoots.Any(root => !Path.IsPathFullyQualified(root))
            || GatewayOrigins is null || GatewayOrigins.Length == 0 || GatewayOrigins.Any(origin => !Uri.TryCreate(origin, UriKind.Absolute, out var uri)
                || (uri.Scheme != "http" && uri.Scheme != "https") || uri.UserInfo != "" || uri.AbsolutePath != "/" || uri.Query != "" || uri.Fragment != "")
            || InitialProbeBytes < 32_768 || ExtendedProbeBytes < InitialProbeBytes || ExtendedProbeBytes > 100_000_000
            || InitialAnalyzeSeconds < 1 || ExtendedAnalyzeSeconds < InitialAnalyzeSeconds || ExtendedAnalyzeSeconds > 30
            || InitialTimeoutSeconds < 1 || ExtendedTimeoutSeconds < 1 || InitialTimeoutSeconds + ExtendedTimeoutSeconds > 90
            || MetadataMaxAgeHours < 1 || MetadataMaxAgeHours > 168)
        {
            throw new InvalidOperationException("Invalid 115 Direct playback policy");
        }
    }
}

public sealed record SourceSnapshot(string Path, string Fingerprint, DateTime ModifiedUtc);
public sealed record MetadataStamp(string Fingerprint, DateTime RefreshedUtc);

public sealed class PlaybackPolicy
{
    private readonly ILogger<PlaybackPolicy> _logger;
    private readonly string _statePath;
    private readonly object _stateLock = new();
    private readonly Dictionary<string, MetadataStamp> _stamps;
    private int _unsupportedEncoderLogged;
    public readonly ConcurrentDictionary<Guid, SemaphoreSlim> ItemLocks = new();
    public PolicyOptions Options { get; }

    public PlaybackPolicy(IApplicationPaths paths, ILogger<PlaybackPolicy> logger)
        : this(Path.Combine(paths.PluginConfigurationsPath, "115-direct-playback.json"),
            Path.Combine(paths.DataPath, "115-direct-playback", "metadata.json"), logger) { }

    public PlaybackPolicy(string optionsPath, string statePath, ILogger<PlaybackPolicy> logger)
    {
        _logger = logger;
        _statePath = statePath;
        try
        {
            Options = File.Exists(optionsPath)
                ? JsonSerializer.Deserialize<PolicyOptions>(File.ReadAllText(optionsPath)) ?? new()
                : new();
            Options.Validate();
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException or InvalidOperationException)
        {
            Options = new() { Enabled = false };
            _logger.LogError("115 Direct playback optimization disabled: invalid/unreadable options ({ErrorType})", error.GetType().Name);
        }
        if (!File.Exists(optionsPath))
        {
            try
            {
                Directory.CreateDirectory(Path.GetDirectoryName(optionsPath)!);
                File.WriteAllText(optionsPath, JsonSerializer.Serialize(Options, new JsonSerializerOptions { WriteIndented = true }));
            }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException)
            {
                _logger.LogWarning("115 Direct options save failed: {ErrorType}", error.GetType().Name);
            }
        }
        try
        {
            _stamps = File.Exists(statePath)
                ? JsonSerializer.Deserialize<Dictionary<string, MetadataStamp>>(File.ReadAllText(statePath)) ?? new()
                : new();
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException)
        {
            _stamps = new();
            _logger.LogWarning("115 Direct metadata index reset: {ErrorType}", error.GetType().Name);
        }
        _logger.LogInformation("115 Direct playback policy enabled={Enabled} small_bytes={SmallBytes} extended_bytes={ExtendedBytes} max_age_hours={MaxAgeHours}",
            Options.Enabled, Options.InitialProbeBytes, Options.ExtendedProbeBytes, Options.MetadataMaxAgeHours);
    }

    public bool IsProjectUrl(string? raw)
    {
        if (!Options.Enabled || !Uri.TryCreate(raw, UriKind.Absolute, out var uri)
            || uri.UserInfo != "" || uri.Fragment != "" || !uri.AbsolutePath.StartsWith("/direct/", StringComparison.Ordinal)
            || !uri.Query.TrimStart('?').Split('&').Any(part => part.StartsWith("sig=", StringComparison.Ordinal) && part.Length > 4))
        {
            return false;
        }
        return Options.GatewayOrigins.Any(origin => Uri.Compare(new Uri(origin), uri, UriComponents.SchemeAndServer,
            UriFormat.Unescaped, StringComparison.OrdinalIgnoreCase) == 0);
    }

    public SourceSnapshot? Snapshot(string? path)
    {
        if (!Options.Enabled || path is null || !path.EndsWith(".strm", StringComparison.OrdinalIgnoreCase)) return null;
        try
        {
            var full = Path.GetFullPath(path);
            if (!Options.StrmRoots.Any(root => full.StartsWith(Path.TrimEndingDirectorySeparator(Path.GetFullPath(root)) + Path.DirectorySeparatorChar, StringComparison.Ordinal))) return null;
            var file = new FileInfo(full);
            if (!file.Exists || file.Length is <= 0 or > 16_384) return null;
            var raw = File.ReadAllText(full).Trim();
            if (!IsProjectUrl(raw)) return null;
            var stamp = $"{full}\0{file.LastWriteTimeUtc.Ticks}\0{file.Length}\0{raw}";
            return new(full, Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(stamp))), file.LastWriteTimeUtc);
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or ArgumentException)
        {
            return null;
        }
    }

    public static bool Usable(IReadOnlyList<MediaStream> streams, long? runtimeTicks, bool audioOnly)
    {
        if (runtimeTicks is null or <= 0) return false;
        var audio = streams.Where(s => s.Type == MediaStreamType.Audio).ToArray();
        if (audioOnly) return audio.Any(s => !string.IsNullOrWhiteSpace(s.Codec));
        return streams.Any(s => s.Type == MediaStreamType.Video && !string.IsNullOrWhiteSpace(s.Codec) && s.Width > 0 && s.Height > 0)
            && (audio.Length == 0 || audio.Any(s => !string.IsNullOrWhiteSpace(s.Codec)));
    }

    public bool CanReuse(Guid itemId, SourceSnapshot snapshot, DateTime lastRefreshedUtc, bool usable)
    {
        if (!usable || File.Exists(RefreshMarker(itemId))) return false;
        lock (_stateLock)
        {
            if (_stamps.TryGetValue(itemId.ToString("N"), out var saved))
            {
                return saved.Fingerprint == snapshot.Fingerprint && saved.RefreshedUtc >= DateTime.UtcNow.AddHours(-Options.MetadataMaxAgeHours);
            }
        }
        // Bootstrap from Jellyfin's persisted metadata only when it was refreshed
        // after this exact STRM file was last modified and is still fresh.
        return lastRefreshedUtc >= snapshot.ModifiedUtc && lastRefreshedUtc <= DateTime.UtcNow.AddMinutes(1)
            && lastRefreshedUtc >= DateTime.UtcNow.AddHours(-Options.MetadataMaxAgeHours);
    }

    public void Remember(Guid itemId, SourceSnapshot snapshot, DateTime? bootstrappedAt = null)
    {
        lock (_stateLock)
        {
            if (bootstrappedAt.HasValue && _stamps.TryGetValue(itemId.ToString("N"), out var old)
                && old.Fingerprint == snapshot.Fingerprint) return;
            _stamps[itemId.ToString("N")] = new(snapshot.Fingerprint, bootstrappedAt ?? DateTime.UtcNow);
            var temporary = _statePath + ".tmp";
            try
            {
                Directory.CreateDirectory(Path.GetDirectoryName(_statePath)!);
                File.WriteAllText(temporary, JsonSerializer.Serialize(_stamps));
                File.Move(temporary, _statePath, true);
                // A successful real probe consumes the operator's one-shot
                // refresh request. Failed/incomplete probes leave it pending.
                if (!bootstrappedAt.HasValue) File.Delete(RefreshMarker(itemId));
            }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException)
            {
                _logger.LogWarning("115 Direct metadata index save failed: {ErrorType}", error.GetType().Name);
            }
        }
    }

    private string RefreshMarker(Guid itemId) => Path.Combine(Path.GetDirectoryName(_statePath)!, "refresh", itemId.ToString("N"));

    public static string ProbeArguments(string original, int bytes, int analyzeSeconds)
    {
        // Jellyfin emits these two options at the start, before any quoted UA.
        // Never replace option-looking strings inside the user's quoted header.
        var remainder = Regex.Replace(original, @"\A\s*(?:(?:-analyzeduration|-probesize)\s+[0-9A-Za-z.]+\s*)*", "");
        return $"-analyzeduration {analyzeSeconds * 1_000_000L} -probesize {bytes} {remainder}".TrimEnd();
    }

    public void LogMetadata(Guid itemId, string mode, double elapsedMs) =>
        _logger.LogInformation("115 Direct metadata item={ItemId} mode={Mode} elapsed_ms={ElapsedMs}", itemId, mode, elapsedMs);

    public void LogUnsupportedEncoder()
    {
        if (Interlocked.Exchange(ref _unsupportedEncoderLogged, 1) == 0)
            _logger.LogWarning("115 Direct bounded probe unavailable for this encoder version; using Jellyfin's original encoder");
    }

    public void LogProbe(string key, string tier, double elapsedMs, bool usable)
    {
        var extended = tier.StartsWith("extended", StringComparison.Ordinal);
        _logger.LogInformation("115 Direct probe source={SourceKey} tier={Tier} probe_bytes={ProbeBytes} analyze_seconds={AnalyzeSeconds} timeout_seconds={TimeoutSeconds} elapsed_ms={ElapsedMs} usable={Usable}",
            key, tier, extended ? Options.ExtendedProbeBytes : Options.InitialProbeBytes,
            extended ? Options.ExtendedAnalyzeSeconds : Options.InitialAnalyzeSeconds,
            extended ? Options.ExtendedTimeoutSeconds : Options.InitialTimeoutSeconds, elapsedMs, usable);
    }
}
