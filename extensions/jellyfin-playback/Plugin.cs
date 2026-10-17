using MediaBrowser.Common.Configuration;
using MediaBrowser.Common.Plugins;
using MediaBrowser.Controller;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.MediaEncoding;
using MediaBrowser.Controller.Plugins;
using Microsoft.Extensions.DependencyInjection;

namespace DirectPlayback;

public sealed class Plugin : BasePlugin
{
    public Plugin(IApplicationPaths paths)
    {
        var assembly = GetType().Assembly;
        SetAttributes(assembly.Location, Path.Combine(paths.DataPath, "115-direct-playback"), assembly.GetName().Version!);
    }

    public override string Name => "115 Direct Playback";
    public override string Description => "Reuse unchanged project STRM metadata and bound necessary remote probes.";
    public override Guid Id => new("2bfd1b9e-45e4-4ec9-9ecf-dc1115ee0096");
}

// Decorate Jellyfin's existing services. Jellyfin still builds the playback
// response, applies user policy/device profiles, chooses tracks and creates the
// play session. No playback response or user token is shared between callers.
public sealed class ServiceRegistrator : IPluginServiceRegistrator
{
    public void RegisterServices(IServiceCollection services, IServerApplicationHost applicationHost)
    {
        var sources = services.LastOrDefault(s => s.ServiceType == typeof(IMediaSourceManager))
            ?? throw new InvalidOperationException("Jellyfin IMediaSourceManager registration missing");
        var encoder = services.LastOrDefault(s => s.ServiceType == typeof(IMediaEncoder))
            ?? throw new InvalidOperationException("Jellyfin IMediaEncoder registration missing");
        services.AddSingleton<PlaybackPolicy>();
        Decorate<IMediaSourceManager>(services, sources);
        Decorate<IMediaEncoder>(services, encoder);
    }

    private static void Decorate<T>(IServiceCollection services, ServiceDescriptor descriptor) where T : class
    {
        services.Remove(descriptor);
        services.Add(new ServiceDescriptor(typeof(T), provider =>
        {
            var original = descriptor.ImplementationInstance
                ?? descriptor.ImplementationFactory?.Invoke(provider)
                ?? ActivatorUtilities.CreateInstance(provider, descriptor.ImplementationType!);
            return ServiceProxy<T>.Wrap((T)original, provider.GetRequiredService<PlaybackPolicy>());
        }, descriptor.Lifetime));
    }
}
