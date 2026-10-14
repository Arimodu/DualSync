import json
from pathlib import Path

def ref(name):
    return {'$ref': '#/components/schemas/' + name}

def string(description=None, enum=None):
    result = {'type': 'string'}
    if description:
        result['description'] = description
    if enum:
        result['enum'] = enum
    return result

def number(minimum=0):
    return {'type': 'number', 'minimum': minimum}

def integer(minimum=0):
    return {'type': 'integer', 'minimum': minimum}

def array(items):
    return {'type': 'array', 'items': items}

def obj(properties, required=None, extra=True):
    return {'type': 'object', 'properties': properties,
            'required': list(properties) if required is None else required,
            'additionalProperties': extra}

boolean = {'type': 'boolean'}
schemas = {}
schemas['Error'] = obj({'error': string(), 'code': string()}, extra=False)
schemas['Error']['properties']['code']['enum'] = ['INVALID_JSON', 'INVALID_CONTENT_TYPE', 'INVALID_ARGUMENT', 'INVALID_PATH', 'INVALID_TRACK', 'AMBIGUOUS_TRACK', 'OUTSIDE_ROOT', 'NOT_FOUND', 'UNREADABLE', 'PROBE_FAILED', 'UNSUPPORTED_MEDIA', 'CROSS_ORIGIN', 'PRIVATE_NETWORK_REQUIRED', 'METHOD_NOT_ALLOWED', 'STREAMS_STOPPED', 'STREAMS_RUNNING', 'NO_MEDIA', 'MEDIA_NOT_READY', 'DESTINATIONS_REQUIRED', 'UNSUPPORTED_HOST', 'UNKNOWN_COMMAND', 'BUSY', 'PUBLISHER_FAILED', 'INTERNAL_ERROR', 'PLEX_FAILED']
schemas['Entry'] = obj({'path': string(), 'name': string(), 'directory': boolean})
schemas['Listing'] = obj({'entries': array(ref('Entry')), 'next_cursor': integer(-1), 'indexing': boolean, 'truncated': boolean})
schemas['Track'] = obj({'index': integer(), 'kind': string(enum=['video', 'audio', 'subtitle']),
    'codec': string(), 'language': string(), 'title': string(), 'channels': integer(),
    'recommended': string(enum=['', 'sub', 'dub']), 'attached': boolean})
schemas['Probe'] = obj({'path': string(), 'duration': number(), 'tracks': array(ref('Track')),
    'title': string(), 'artist': string(), 'album_artist': string(), 'album': string()})
schemas['Source'] = obj({'path': {'type': 'string', 'minLength': 1},
    'audio': {'type': ['integer', 'null'], 'minimum': -1,
              'description': 'Absolute FFmpeg stream index; -1 selects silence; null/omitted recommends language then first audio.'},
    'subtitle': {'type': ['integer', 'null'], 'minimum': -1,
                 'description': 'Absolute stream index; -1 disables subtitles; null/omitted uses recommendation, otherwise Off.'}}, ['path'], False)
schemas['PlexSource'] = obj({'id': dict(string(), pattern='^[0-9]+$'), 'media_id': dict(string(), pattern='^[0-9]+$'), 'part_id': dict(string(), pattern='^[0-9]+$'), 'server_id': string()}, ['id'], False)
schemas['VideoMetadata'] = obj({'id': string(), 'matched': boolean, 'type': string(), 'title': string(), 'show': string(), 'season': integer(), 'episode': integer(), 'year': integer(), 'summary': string(), 'identifiers': array(string()), 'cover_url': string(), 'poster_url': string()}, extra=False)
schemas['Source']['properties'].update({'plex':ref('PlexSource'), 'video': {'type':['integer','null'],'minimum':0}, 'transport': dict(string(enum=['local','plex']), readOnly=True)})
schemas['Source']['required']=[]
schemas['Source']['oneOf']=[{'required':['path'],'not':{'required':['plex']}},{'required':['plex'],'not':{'required':['path']}}]
schemas['Item'] = obj({'title': string(), 'sub': ref('Source'), 'dub': ref('Source'), 'metadata_source':string(enum=['sub','dub']), 'metadata':dict(ref('VideoMetadata'),readOnly=True)}, ['sub', 'dub'], False)
schemas['Load'] = obj({'mode': string(enum=['single', 'paired']), 'title': string(),
    'start_index': dict(integer(), default=0), 'items': dict(array(ref('Item')), minItems=1, maxItems=500)}, ['mode', 'items'], False)
schemas['PreviewRequest'] = obj({'sub_folder': string(), 'dub_folder': dict(string(), default='')}, ['sub_folder'], False)
schemas['Preview'] = obj({'items': array(ref('Item')), 'unmatched': array(string()), 'ambiguous': array(string())})
settings = {'sub_server': string('RTMP/RTSP base URL without path, query or embedded credentials. Blank means unconfigured.'),
    'dub_server': string(), 'stream_path': {'type': 'string', 'pattern': '^[A-Za-z0-9][A-Za-z0-9_./-]*$'},
    'sub_username': string(), 'dub_username': string(), 'music_folder': string('Root-relative directory; blank disables music.'),
    'music_volume': dict(number(), maximum=100), 'hud_enabled': boolean, 'music_source': string(enum=['folder','plex','off']), 'music_shuffle': boolean, 'plex_server': string(), 'plex_playlist_id': string()}
schemas['SettingsPatch'] = obj(dict(settings, sub_password=dict(string(), writeOnly=True), dub_password=dict(string(), writeOnly=True), plex_token=dict(string(), writeOnly=True)), [], False)
schemas['PublicSettings'] = obj(dict(settings, sub_password_set=boolean, dub_password_set=boolean, plex_token_set=boolean), extra=False)
schemas['Command'] = obj({'action': string(enum=['start', 'stop', 'play', 'pause', 'pause_silent', 'seek', 'episode', 'unload', 'music_play', 'music_pause', 'music_next', 'music_refresh', 'rescan']),
    'value': {'type': ['number', 'null'], 'description': 'seek accepts -10, 10 or 80 seconds; episode accepts a one-based playlist item; omit for other actions.'}}, ['action'], False)
schemas['Output'] = obj({'ready': boolean, 'pid': integer(), 'out_time': number(), 'restarts': integer(),
    'error': string(), 'queued_frames': dict(integer(), maximum=8), 'encoder': string(enum=['nvenc', 'qsv', 'vaapi', 'cpu'])})
schemas['Operation'] = obj({'id': integer(), 'state': string(enum=['idle', 'running', 'complete', 'failed']), 'error': string()})
schemas['Media'] = obj({'phase': string(enum=['none', 'loading', 'ready', 'playing', 'paused', 'resuming', 'seeking', 'buffering', 'ended']),
    'mode': string(enum=['', 'single', 'paired']), 'title': string(), 'index': integer(), 'total': integer(),
    'position': number(), 'duration': number(), 'items': array(ref('Item')), 'operation': ref('Operation')})
schemas['Music'] = obj({'active': boolean, 'paused': dict(boolean, description='Explicit music hold set by music_pause or pause_silent. music_play and normal pause clear it. Media playback still holds music independently.'), 'title': string(), 'artist': string(), 'album_artist': string(), 'album': string(),
    'position': number(), 'duration': number(), 'gain': dict(number(), maximum=1), 'cover_url': string(), 'path': string(), 'source':string(enum=['folder','plex','off']), 'playlist_id':string(), 'playlist_title':string(), 'ready_count':dict(integer(),maximum=3), 'preparation_state':string(enum=['idle','preparing','ready','failed']), 'error':string(), 'upcoming':array(ref('MusicUpcoming'))})
schemas['MusicUpcoming']=obj({'path':string(),'title':string(),'artist':string(),'duration':number(),'cover_url':string()})
schemas['Media']['properties']['metadata']=ref('VideoMetadata')
schemas['Error']['properties']['code']['enum'] += ['AMBIGUOUS_VERSION','PLEX_SERVER_CHANGED','METADATA_CONFLICT']
schemas['PlexLibraries']=obj({'libraries':array(obj({'id':string(),'title':string(),'type':string(enum=['movie','show'])}))})
schemas['PlexVideos']=obj({'entries':array(obj({'id':string(),'type':string(),'title':string(),'season':integer(),'episode':integer()})),'next_cursor':integer(-1)})
schemas['PlexVideoDetails']=obj({'server_id':string(),'metadata':ref('VideoMetadata'),'versions':array(obj({'id':string(),'resolution':string(),'video_codec':string(),'audio_codec':string(),'parts':array(obj({'id':string(),'name':string(),'size':integer()}))}))})
schemas['PlexVideoProbe']=obj({'probe':ref('Probe'),'source':ref('Source'),'metadata':ref('VideoMetadata')})
schemas['VideoCollection']=obj({'folder':string(),'plex_season_id':dict(string(),pattern='^[0-9]+$')},[],False)
schemas['VideoCollection']['oneOf']=[{'required':['folder'],'not':{'required':['plex_season_id']}},{'required':['plex_season_id'],'not':{'required':['folder']}}]
schemas['VideoPreviewRequest']=obj({'sub':ref('VideoCollection'),'dub':ref('VideoCollection')},['sub'],False)
schemas['PlexPlaylist']=obj({'id':string(),'title':string(),'smart':boolean,'count':integer()})
schemas['PlexTrack']=obj({'id':string(),'title':string(),'artist':string(),'album_artist':string(),'album':string(),'duration':number(),'file':string()})
schemas['PlexPlaylists']=obj({'playlists':array(ref('PlexPlaylist'))})
schemas['PlexTracks']=obj({'tracks':array(ref('PlexTrack'))})
schemas['Encoder'] = obj({'kind': string(enum=['', 'nvenc', 'qsv', 'vaapi', 'cpu']), 'device': string()})
schemas['CPU'] = obj({'virtual': boolean, 'virtualization': string(), 'physical_cores': integer(), 'usable_vcpus': integer(),
    'budget': integer(), 'eligible': boolean, 'basis': string()})
schemas['EncoderTest'] = obj({'encoder': ref('Encoder'), 'name': string(), 'available': boolean, 'tested': boolean,
    'passed': boolean, 'fps_per_output': number(), 'error': string()})
schemas['Diagnostics'] = obj({'supported': boolean, 'cpu_fallback': boolean, 'selected': ref('Encoder'), 'cpu': ref('CPU'),
    'tests': array(ref('EncoderTest')), 'missing': array(string()), 'ffmpeg': string(), 'fingerprint': string(),
    'created': string(), 'warning': string(), 'running': boolean})
schemas['Status'] = obj({'revision': integer(), 'mode': string(enum=['stopped', 'idle', 'playing', 'paused', 'ended']),
    'buffering': boolean, 'episode': {'type': ['integer', 'null']}, 'position': number(), 'duration': number(),
    'episodes': array(obj({'episode': integer(1), 'duration': number()})), 'settings': ref('PublicSettings'),
    'music_track': string(), 'error': string(), 'pause_seconds': number(),
    'outputs': obj({'sub': ref('Output'), 'dub': ref('Output')}, [], False), 'media': ref('Media'),
    'music': ref('Music'), 'diagnostics': ref('Diagnostics'), 'renderer': obj({'p95_ms': number(), 'samples': integer()})})
schemas['Empty'] = obj({}, [], False)
paths = {}

def operation(path, method, operation_id, result, description='', request=None, code=200, parameters=None, example=None):
    content = {'schema': ref(request)} if request else None
    if content is not None and example is not None:
        content['example'] = example
    value = {'operationId': operation_id, 'description': description, 'responses': {
        str(code): {'description': 'Current snapshot or result', 'content': {'application/json': {'schema': ref(result)}}},
        'default': {'description': '400 invalid input; 403 origin/network/root restriction; 404 missing; 405 method; 409 state conflict; 415 content type; 422 unusable media; 500 internal; 502 publisher creation failure.',
                    'content': {'application/json': {'schema': ref('Error')}}}}}
    if content:
        value['requestBody'] = {'required': True, 'content': {'application/json': content}}
    if parameters:
        value['parameters'] = parameters
    paths.setdefault(path, {})[method] = value

def query(name, schema, required=False):
    return {'name': name, 'in': 'query', 'required': required, 'schema': schema}

operation('/api/plex/playlists','get','listPlexPlaylists','PlexPlaylists','Audio playlists visible to configured Plex token.')
operation('/api/plex/libraries','get','listPlexVideoLibraries','PlexLibraries','Movie and show libraries visible to configured account.')
operation('/api/plex/videos','get','browsePlexVideos','PlexVideos','Browse a library or show/season children. Search title within that collection. next_cursor=-1 ends pagination.',parameters=[query('library_id',string()),query('parent_id',string()),query('q',string()),query('cursor',integer()),query('limit',dict(integer(1),maximum=200))])
operation('/api/plex/video','get','getPlexVideo','PlexVideoDetails','Metadata and all versions/parts. Multipart versions cannot be loaded.',parameters=[query('id',string(),True)])
operation('/api/plex/video/probe','get','probePlexVideo','PlexVideoProbe','Verified FFmpeg indices, resolved source and metadata. Cold probe can take 45 seconds; runs outside publisher clock. audio/subtitle remain unset for frontend selection.',parameters=[query('id',string(),True),query('media_id',string()),query('part_id',string()),query('server_id',string())])
operation('/api/media/preview','post','previewVideoSources','Preview','Season/folder pairing by season and episode. Missing season numbers can inherit the only explicit season. Returned title is blank so matched metadata labels loading. Missing/ambiguous entries require correction.', 'VideoPreviewRequest')
operation('/api/plex/artwork','get','getPlexVideoArtwork','Error',parameters=[query('id',string(),True),query('kind',string(enum=['poster'])),query('server_id',string())])
paths['/api/plex/artwork']['get']['responses']['200']={'description':'Plex image, up to 10 MiB. Use returned artwork URL.', 'content':{'image/jpeg':{'schema':{'type':'string','format':'binary'}},'image/png':{'schema':{'type':'string','format':'binary'}}}}
operation('/api/plex/playlist','get','previewPlexPlaylist','PlexTracks','Current playlist tracks; duration in seconds. file is the Plex server path, not a library-relative path.',parameters=[query('id',string(),True)])
operation('/api/status', 'get', 'getStatus', 'Status', 'Poll and discard older revisions. All times are seconds. Publishers have a separate lifetime from selected media.')
operation('/api/control', 'post', 'control', 'Status', 'Playback controls do not close publishers. Only stop disconnects streams.', 'Command', example={'action': 'seek', 'value': 80})
operation('/api/settings', 'post', 'updateSettings', 'Status', 'Partial update. HUD, music and Plex settings can change while publishing. Destination changes require stopped streams. Blank/omitted passwords preserve stored values; status never returns passwords.', 'SettingsPatch')
operation('/api/library', 'get', 'browseLibrary', 'Listing', 'Search recursively within path. Results are partial during indexing; restart pagination when indexing finishes. next_cursor=-1 marks last page. Index limit 200000 entries; truncated reports limit.', parameters=[query('path', dict(string(), default='')), query('q', dict(string(), default='')), query('cursor', dict(integer(), default=0)), query('limit', dict(integer(1), maximum=200, default=100))])
operation('/api/library/probe', 'get', 'probeMedia', 'Probe', 'Cold probe takes up to 30 seconds without blocking publishing. Select absolute stream indices, not list positions.', parameters=[query('path', dict(string(), minLength=1), True)])
operation('/api/library/playlist', 'post', 'previewPlaylist', 'Preview', 'Direct child files, naturally ordered. Empty dub_folder previews single-file mode; paired folders require explicit identifiers. Preview does not select media.', 'PreviewRequest')
operation('/api/media/load', 'post', 'loadMedia', 'Status', 'Asynchronous replacement, reset to zero, then Ready requiring Play. Poll media.operation; newer load/unload cancels old operation. single requires identical media references; paired accepts Plex/storage combinations. Conflicting matched identities require metadata_source.', 'Load', 202, example={'mode': 'single', 'start_index': 0, 'items': [{'title': 'Episode 1', 'sub': {'path': 'Anime/Example/S01E01.mkv', 'audio': 1, 'subtitle': 3}, 'dub': {'path': 'Anime/Example/S01E01.mkv', 'audio': 2, 'subtitle': 4}}]})
operation('/api/media/unload', 'post', 'unloadMedia', 'Status', 'Clear media and retain publishers; no-media scene and idle music.', 'Empty', example={})
operation('/api/diagnostics', 'get', 'getDiagnostics', 'Diagnostics')
operation('/api/diagnostics', 'post', 'runDiagnostics', 'Diagnostics', 'Streams must be stopped. Returns running=true; poll until false. May take several minutes. Short test is not a long-term stability guarantee.', 'Empty', 202, example={})
operation('/api/music/art/{id}', 'get', 'getMusicArtwork', 'Error', parameters=[{'name': 'id', 'in': 'path', 'required': True, 'schema': {'type': 'string', 'pattern': '^[a-f0-9]{32}$'}}])
paths['/api/music/art/{id}']['get']['responses']['200'] = {'description': 'Cached artwork; use music.cover_url verbatim.', 'content': {'image/png': {'schema': {'type': 'string', 'format': 'binary'}}}}
operation('/api/openapi.yaml', 'get', 'getOpenAPI', 'Error', 'Also available when frontend serving is disabled.')
paths['/api/openapi.yaml']['get']['responses']['200'] = {'description': 'This specification', 'content': {'application/yaml': {'schema': {'type': 'string'}}}}
spec = {'openapi': '3.1.0', 'info': {'title': 'DualSync API', 'version': '0.4.0',
    'description': 'Private-network, same-origin API. No CORS. File paths are root-relative using forward slashes. Media index is zero-based; legacy episode control is one-based. Passwords are write-only.'},
    'servers': [{'url': '/'}], 'paths': paths, 'components': {'schemas': schemas}}
Path(__file__).resolve().parents[1].joinpath('api/openapi.yaml').write_text(json.dumps(spec, indent=2) + '\n', encoding='utf-8')
