class StarWarsAvatarsController < ApplicationController

    def show
      @starwavatar_avatar = StarwavatarAvatar.new(params[:id])
      response.headers['Content-Type'] = 'image/svg+xml'
      render inline: @starwavatar_avatar.svg
    end

end
